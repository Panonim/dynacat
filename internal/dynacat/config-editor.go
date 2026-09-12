package dynacat

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"gopkg.in/yaml.v3"
)

var editorConfigWarnOnce sync.Once

// editorEnabledFromEnv reports whether the web UI editor is available at all.
func editorEnabledFromEnv() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ENABLE_EDITOR"))) {
	case "false", "0", "f", "no", "off":
		return false
	}
	return true
}

func warnAboutIgnoredEditorConfig(c *config) {
	var ignored []string

	if c.Server.AllowEditing {
		ignored = append(ignored, "server.allow-editing")
	}
	if len(c.Server.EditingUsers) > 0 {
		ignored = append(ignored, "server.editing-users")
	}
	if len(c.Server.EditingGroups) > 0 {
		ignored = append(ignored, "server.editing-groups")
	}
	for username, u := range c.Auth.Users {
		if u != nil && len(u.RestrictEditing) > 0 {
			ignored = append(ignored, fmt.Sprintf("auth.users.%s.restrict-editing", username))
		}
	}
	if len(ignored) == 0 {
		return
	}

	slices.Sort(ignored)
	editorConfigWarnOnce.Do(func() {
		slog.Warn("Editor is disabled through ENABLE_EDITOR, editor-specific configuration is ignored",
			"settings", strings.Join(ignored, ", "))
	})
}

var editorWarnedAboutExposure bool

func (a *application) warnAboutEditorExposure() {
	warnOnce(
		a.EditorEnabled && a.Config.Server.AllowEditing && !a.RequiresAuth,
		&editorWarnedAboutExposure,
		"The web UI editor is enabled without authentication. Anyone who can reach this server can rewrite your config. Set server.allow-editing to false or configure auth.",
	)
}

type editorConfigView struct {
	Pages        []editorPageView   `json:"pages"`
	Theme        map[string]string  `json:"theme"`
	Branding     map[string]string  `json:"branding"`
	ThemePresets []editorPresetView `json:"themePresets"`
	MainWritable bool               `json:"mainWritable"`
}

type editorPresetView struct {
	Key    string            `json:"key"`
	Values map[string]string `json:"values"`
}

type editorPageView struct {
	Title    string             `json:"title"`
	Slug     string             `json:"slug"`
	Width    string             `json:"width"`
	File     string             `json:"file"`
	Writable bool               `json:"writable"`
	Options  map[string]string  `json:"options"`
	Columns  []editorColumnView `json:"columns"`
}

type editorColumnView struct {
	Size    string             `json:"size"`
	Widgets []editorWidgetView `json:"widgets"`
}

type editorWidgetView struct {
	Type       string             `json:"type"`
	Title      string             `json:"title"`
	Values     map[string]string  `json:"values"`
	Structured map[string]any     `json:"structured,omitempty"`
	Widgets    []editorWidgetView `json:"widgets,omitempty"`
}

type editorMutation struct {
	Op         string            `json:"op"`
	Page       int               `json:"page"`
	Column     int               `json:"column"`
	Index      int               `json:"index"`
	Path       []int             `json:"path"`
	ToPath     []int             `json:"toPath"`
	Size       string            `json:"size"`
	WidgetType string            `json:"widgetType"`
	Fields     map[string]any    `json:"fields"`
	RawFields  map[string]string `json:"rawFields"`
	Title      string            `json:"title"`
	Layout     []string          `json:"layout"`
	Theme      map[string]any    `json:"theme"`
	Branding   map[string]any    `json:"branding"`
	PresetKey  string            `json:"presetKey"`
}

type editorPermissionError struct{ path string }

func (e *editorPermissionError) Error() string {
	return fmt.Sprintf("cannot write %s: the config directory is read only or lacks write permission", filepath.Base(e.path))
}

type editorDisabledError struct{}

func (e *editorDisabledError) Error() string {
	return "editing this page through the web UI is disabled"
}

// userRestrictEditing returns user's restrict-editing slugs. Only password-based users
// can have them, since OIDC users have no auth.users entry.
func (a *application) userRestrictEditing(user *authenticatedUser) []string {
	if user == nil {
		return nil
	}
	if u := a.Config.Auth.Users[user.Username]; u != nil {
		return u.RestrictEditing
	}
	return nil
}

// EditingAllowedForPage reports whether user may edit page p (restrict-editing/allow-editing rules).
func (a *application) EditingAllowedForPage(user *authenticatedUser, p *page) bool {
	if !a.EditorEnabled {
		return false
	}
	if p == nil || !a.canUserAccessPage(user, p) {
		return false
	}
	if restrict := a.userRestrictEditing(user); len(restrict) > 0 {
		return slices.Contains(restrict, p.Slug)
	}
	return a.Config.Server.AllowEditing
}

func (a *application) editingAllowedForPageIndex(user *authenticatedUser, i int) bool {
	if i < 0 || i >= len(a.Config.Pages) {
		return false
	}
	return a.EditingAllowedForPage(user, &a.Config.Pages[i])
}

// UserAllowedToEdit reports whether user may use the web UI editor at all (editing-users/editing-groups).
// Password-based users have no groups, so editing-groups only ever matches OIDC users.
func (a *application) UserAllowedToEdit(user *authenticatedUser) bool {
	if !a.EditorEnabled {
		return false
	}
	users := a.Config.Server.EditingUsers
	groups := a.Config.Server.EditingGroups
	if len(users) == 0 && len(groups) == 0 {
		return true
	}
	if user == nil {
		return false
	}
	if slices.Contains(users, user.Username) {
		return true
	}
	for _, group := range groups {
		if slices.Contains(user.Groups, group) {
			return true
		}
	}
	return false
}

// Whether the user can edit anything at all. restrict-editing users keep their pages even
// when allow-editing is off, which is how EditingAllowedForPage treats them.
func (a *application) userCanEditAnything(user *authenticatedUser) bool {
	if !a.UserAllowedToEdit(user) {
		return false
	}

	return a.Config.Server.AllowEditing || len(a.userRestrictEditing(user)) > 0
}

func isWriteBlockedError(err error) bool {
	return os.IsPermission(err) || errors.Is(err, syscall.EROFS)
}

type editorValidationError struct {
	err   error
	field string
}

func (e *editorValidationError) Error() string { return e.err.Error() }

func (a *application) buildEditorConfigView(user *authenticatedUser) (editorConfigView, error) {
	mainPath := a.configPath
	mainDoc, err := loadYAMLDocument(mainPath)
	if err != nil {
		return editorConfigView{}, err
	}

	pagesNode := getMappingValue(documentRoot(mainDoc), "pages")
	if pagesNode == nil || pagesNode.Kind != yaml.SequenceNode {
		return editorConfigView{}, fmt.Errorf("config has no pages")
	}

	view := editorConfigView{}
	docs := newEditorDocs()
	for i := range pagesNode.Content {
		// Positions have to line up with the page indices mutations are addressed by, so a
		// page the user cannot see is emitted as an empty slot rather than skipped.
		if i < len(a.Config.Pages) && !a.canUserAccessPage(user, &a.Config.Pages[i]) {
			view.Pages = append(view.Pages, editorPageView{Options: map[string]string{}})
			continue
		}

		path, _, pageNode, err := resolvePageNode(mainDoc, mainPath, i)
		if err != nil {
			return editorConfigView{}, err
		}
		pv := pageNodeToView(pageNode, path, docs)
		if i < len(a.Config.Pages) {
			pv.Slug = a.Config.Pages[i].Slug
			pv.Title = a.Config.Pages[i].Title
		}
		view.Pages = append(view.Pages, pv)
	}

	root := documentRoot(mainDoc)
	view.Theme = sectionToStringMap(root, "theme")
	view.Branding = sectionToStringMap(root, "branding")
	view.ThemePresets = presetsToViews(root)
	view.MainWritable = pathWritable(mainPath)

	return view, nil
}

func sectionToStringMap(root *yaml.Node, key string) map[string]string {
	out := map[string]string{}
	section := getMappingValue(root, key)
	if section == nil || section.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(section.Content); i += 2 {
		if v := section.Content[i+1]; v.Kind == yaml.ScalarNode {
			out[section.Content[i].Value] = v.Value
		}
	}
	return out
}

func presetsToViews(root *yaml.Node) []editorPresetView {
	out := []editorPresetView{}
	theme := getMappingValue(root, "theme")
	if theme == nil {
		return out
	}
	presets := getMappingValue(theme, "presets")
	if presets == nil || presets.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(presets.Content); i += 2 {
		keyNode, valNode := presets.Content[i], presets.Content[i+1]
		if valNode.Kind != yaml.MappingNode {
			continue
		}
		vals := map[string]string{}
		for j := 0; j+1 < len(valNode.Content); j += 2 {
			if s := valNode.Content[j+1]; s.Kind == yaml.ScalarNode {
				vals[valNode.Content[j].Value] = s.Value
			}
		}
		out = append(out, editorPresetView{Key: keyNode.Value, Values: vals})
	}
	return out
}

func pageNodeToView(pageNode *yaml.Node, path string, docs *editorDocs) editorPageView {
	pv := editorPageView{
		Title:    scalarValue(getMappingValue(pageNode, "name")),
		Slug:     scalarValue(getMappingValue(pageNode, "slug")),
		Width:    scalarValue(getMappingValue(pageNode, "width")),
		File:     filepath.Base(path),
		Writable: pathWritable(path),
		Options:  map[string]string{},
	}

	for i := 0; i+1 < len(pageNode.Content); i += 2 {
		if v := pageNode.Content[i+1]; v.Kind == yaml.ScalarNode {
			pv.Options[pageNode.Content[i].Value] = v.Value
		}
	}

	columns := getMappingValue(pageNode, "columns")
	if columns == nil {
		return pv
	}

	for _, col := range columns.Content {
		cv := editorColumnView{Size: scalarValue(getMappingValue(col, "size"))}
		if widgets := getMappingValue(col, "widgets"); widgets != nil {
			for _, slot := range expandWidgetSlots(widgets, path, docs, 0) {
				cv.Widgets = append(cv.Widgets, widgetNodeToView(slot.node, slot.ownerPath, docs))
			}
		}
		pv.Columns = append(pv.Columns, cv)
	}

	return pv
}

func widgetNodeToView(w *yaml.Node, ownerPath string, docs *editorDocs) editorWidgetView {
	wv := editorWidgetView{Values: map[string]string{}}
	for i := 0; i+1 < len(w.Content); i += 2 {
		key, val := w.Content[i].Value, w.Content[i+1]
		switch key {
		case "type":
			wv.Type = val.Value
		case "widgets":
			if val.Kind == yaml.SequenceNode {
				for _, slot := range expandWidgetSlots(val, ownerPath, docs, 0) {
					wv.Widgets = append(wv.Widgets, widgetNodeToView(slot.node, slot.ownerPath, docs))
				}
			}
		case "title":
			wv.Title = val.Value
			wv.Values[key] = nodeToText(val)
		default:
			wv.Values[key] = nodeToText(val)
		}

		if key != "widgets" && val.Kind == yaml.SequenceNode {
			var decoded []any
			if err := val.Decode(&decoded); err == nil {
				if wv.Structured == nil {
					wv.Structured = map[string]any{}
				}
				wv.Structured[key] = decoded
			}
		}
	}
	return wv
}

func (a *application) applyEditorMutation(user *authenticatedUser, m editorMutation) error {
	a.editorMu.Lock()
	defer a.editorMu.Unlock()

	switch m.Op {
	case "addPage", "editStyling", "deleteThemePreset":
		// Not scoped to an existing page, so a user restricted to specific pages
		// can't do them and editing must be allowed globally.
		if len(a.userRestrictEditing(user)) > 0 || !a.Config.Server.AllowEditing {
			return &editorDisabledError{}
		}
	default:
		if !a.editingAllowedForPageIndex(user, m.Page) {
			return &editorDisabledError{}
		}
	}

	mainPath := a.configPath
	mainDoc, err := loadYAMLDocument(mainPath)
	if err != nil {
		return err
	}

	if m.Op == "addPage" {
		if separatePageFilesEnabled() {
			return a.addPageFile(mainDoc, mainPath, m)
		}
		return a.addPageInline(mainDoc, mainPath, m)
	}

	if m.Op == "editStyling" {
		root := documentRoot(mainDoc)
		if m.PresetKey != "" {
			applyThemePreset(root, m.PresetKey, m.Theme)
		} else {
			applyStylingSection(root, "theme", m.Theme)
			applyStylingSection(root, "branding", m.Branding)
		}
		return a.writeConfigCandidate(mainPath, marshalDocument(mainDoc))
	}

	if m.Op == "deleteThemePreset" {
		root := documentRoot(mainDoc)
		if m.PresetKey == "" {
			resetBaseTheme(root)
		} else {
			removeThemePreset(root, m.PresetKey)
		}
		return a.writeConfigCandidate(mainPath, marshalDocument(mainDoc))
	}

	if m.Op == "editPage" {
		path, doc, pageNode, err := resolvePageNode(mainDoc, mainPath, m.Page)
		if err != nil {
			return err
		}
		if err := applyPageFields(pageNode, m.Fields); err != nil {
			return err
		}
		setBlockStyleDeep(pageNode)
		return a.writeConfigCandidate(path, marshalDocument(doc))
	}

	if m.Op == "removePage" {
		pages := getMappingValue(documentRoot(mainDoc), "pages")
		if pages == nil || !validIndex(pages.Content, m.Page) {
			return fmt.Errorf("page %d out of range", m.Page)
		}
		includeFile := includeTarget(pages.Content[m.Page])
		pages.Content = removeNode(pages.Content, m.Page)
		if err := a.writeConfigCandidate(mainPath, marshalDocument(mainDoc)); err != nil {
			return err
		}
		if includeFile != "" {
			includePath := includeFile
			if !filepath.IsAbs(includePath) {
				includePath = filepath.Join(filepath.Dir(mainPath), includeFile)
			}
			os.Remove(includePath)
		}
		return nil
	}

	path, doc, pageNode, err := resolvePageNode(mainDoc, mainPath, m.Page)
	if err != nil {
		return err
	}

	columns := getMappingValue(pageNode, "columns")
	if columns == nil || columns.Kind != yaml.SequenceNode {
		return fmt.Errorf("page %d has no columns", m.Page)
	}

	docs := newEditorDocs()
	docs.track(path, doc)
	docs.pagePath = filepath.Clean(path)

	if err := mutateColumns(columns, m, docs); err != nil {
		return err
	}

	setBlockStyleDeep(pageNode)

	return a.writeEditorDocs(docs)
}

func setBlockStyleDeep(n *yaml.Node) {
	if n == nil {
		return
	}
	if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
		n.Style = 0
	}
	for _, c := range n.Content {
		setBlockStyleDeep(c)
	}
}

func mutateColumns(columns *yaml.Node, m editorMutation, docs *editorDocs) error {
	switch m.Op {
	case "addColumn":
		col := newMappingNode()
		addPair(col, "size", scalarNode(orDefault(m.Size, "full")))
		addPair(col, "widgets", sequenceNode())
		columns.Content = insertNode(columns.Content, m.Index, col)
		return nil
	case "removeColumn":
		if !validIndex(columns.Content, m.Column) {
			return fmt.Errorf("column %d out of range", m.Column)
		}
		columns.Content = removeNode(columns.Content, m.Column)
		return nil
	}

	widgets, owner, err := resolveWidgets(columns, m.Path, docs)
	if err != nil {
		return err
	}

	index := m.Path[len(m.Path)-1]
	slots := expandWidgetSlots(widgets, owner, docs, 0)

	switch m.Op {
	case "addWidget":
		node, err := buildWidgetNode(m.WidgetType, m.Fields, m.RawFields)
		if err != nil {
			return err
		}
		seq, at := widgetInsertPoint(slots, widgets, index)
		seq.Content = insertNode(seq.Content, at, node)
	case "editWidget":
		if !validSlotIndex(slots, index) {
			return fmt.Errorf("widget index out of range")
		}
		if err := editWidgetNode(slots[index].node, m.Fields, m.RawFields); err != nil {
			return err
		}
	case "removeWidget":
		if !validSlotIndex(slots, index) {
			return fmt.Errorf("widget index out of range")
		}
		slot := slots[index]
		slot.seq.Content = removeNode(slot.seq.Content, slot.index)
		pruneEmptyInclude(slot)
	case "moveWidget":
		if isIntPrefix(m.Path, m.ToPath) {
			return fmt.Errorf("cannot move a container into itself")
		}
		if !validSlotIndex(slots, index) {
			return fmt.Errorf("widget index out of range")
		}
		slot := slots[index]
		dstWidgets, dstOwner, err := resolveWidgets(columns, m.ToPath, docs)
		if err != nil {
			return err
		}
		dstSlots := expandWidgetSlots(dstWidgets, dstOwner, docs, 0)
		dstSeq, dstIndex := widgetInsertPoint(dstSlots, dstWidgets, m.ToPath[len(m.ToPath)-1])

		slot.seq.Content = removeNode(slot.seq.Content, slot.index)
		if dstSeq == slot.seq && dstIndex > slot.index {
			dstIndex--
		}
		dstSeq.Content = insertNode(dstSeq.Content, dstIndex, slot.node)
		pruneEmptyInclude(slot)
	default:
		return fmt.Errorf("unknown op: %s", m.Op)
	}
	return nil
}

func pruneEmptyInclude(slot widgetSlot) {
	if slot.seq == slot.outerSeq || len(slot.seq.Content) > 0 {
		return
	}
	if validIndex(slot.outerSeq.Content, slot.outerIdx) && includeTarget(slot.outerSeq.Content[slot.outerIdx]) != "" {
		slot.outerSeq.Content = removeNode(slot.outerSeq.Content, slot.outerIdx)
	}
}

func validSlotIndex(slots []widgetSlot, index int) bool {
	return index >= 0 && index < len(slots)
}

func resolveWidgets(columns *yaml.Node, path []int, docs *editorDocs) (*yaml.Node, string, error) {
	if len(path) < 2 {
		return nil, "", fmt.Errorf("invalid widget path")
	}

	widgets := columnWidgets(columns, path[0])
	if widgets == nil {
		return nil, "", fmt.Errorf("column %d out of range", path[0])
	}

	owner := docs.pagePath
	for _, wi := range path[1 : len(path)-1] {
		slots := expandWidgetSlots(widgets, owner, docs, 0)
		if !validSlotIndex(slots, wi) {
			return nil, "", fmt.Errorf("widget %d out of range", wi)
		}
		container := slots[wi]
		inner := getMappingValue(container.node, "widgets")
		if inner == nil {
			inner = sequenceNode()
			addPair(container.node, "widgets", inner)
		}
		widgets, owner = inner, container.ownerPath
	}

	return widgets, owner, nil
}

func isIntPrefix(prefix, full []int) bool {
	if len(prefix) > len(full) {
		return false
	}
	for i := range prefix {
		if prefix[i] != full[i] {
			return false
		}
	}
	return true
}

func applyPageFields(pageNode *yaml.Node, fields map[string]any) error {
	for _, k := range sortedKeys(fields) {
		v := fields[k]
		if k == "name" && isEmptyValue(v) {
			continue
		}
		if isEmptyValue(v) || v == false {
			removeMappingKey(pageNode, k)
			continue
		}
		node := valueNode(v)
		if err := rejectIncludeDirective(k, node); err != nil {
			return err
		}
		setPageMappingKey(pageNode, k, node)
	}

	return nil
}

// parseYAMLIncludes is a textual preprocessor that matches any line, so an include smuggled in
// as a key or inside a multi-line value would make the loader read, and removePage delete, an
// arbitrary file.
func rejectIncludeDirective(key string, node *yaml.Node) error {
	if key == "$include" || key == "!include" || configIncludePattern.MatchString(nodeToText(node)) {
		return &editorValidationError{err: fmt.Errorf("field %s must not contain an include directive", key), field: key}
	}

	return nil
}

func setPageMappingKey(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = value
			return
		}
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == "columns" {
			out := make([]*yaml.Node, 0, len(m.Content)+2)
			out = append(out, m.Content[:i]...)
			out = append(out, scalarNode(key), value)
			out = append(out, m.Content[i:]...)
			m.Content = out
			return
		}
	}
	addPair(m, key, value)
}

func newPageMapping(m editorMutation) *yaml.Node {
	page := newMappingNode()
	addPair(page, "name", scalarNode(orDefault(m.Title, "New Page")))

	columns := sequenceNode()
	layout := m.Layout
	if len(layout) == 0 {
		layout = []string{"full"}
	}
	for _, size := range layout {
		col := newMappingNode()
		addPair(col, "size", scalarNode(size))
		addPair(col, "widgets", sequenceNode())
		columns.Content = append(columns.Content, col)
	}
	addPair(page, "columns", columns)
	return page
}

func separatePageFilesEnabled() bool {
	switch os.Getenv("EDITOR_SEPARATE_PAGE_FILES") {
	case "false", "0", "f":
		return false
	}
	return true
}

func (a *application) addPageInline(mainDoc *yaml.Node, mainPath string, m editorMutation) error {
	root := documentRoot(mainDoc)
	pages := getMappingValue(root, "pages")
	if pages == nil {
		pages = sequenceNode()
		addPair(root, "pages", pages)
	}

	page := newPageMapping(m)
	setBlockStyleDeep(page)
	pages.Content = append(pages.Content, page)

	return a.writeConfigCandidate(mainPath, marshalDocument(mainDoc))
}

func (a *application) addPageFile(mainDoc *yaml.Node, mainPath string, m editorMutation) error {
	pages := getMappingValue(documentRoot(mainDoc), "pages")
	if pages == nil {
		pages = sequenceNode()
		addPair(documentRoot(mainDoc), "pages", pages)
	}

	dir := filepath.Dir(mainPath)
	file := uniquePageFileName(dir, orDefault(m.Title, "New Page"))
	pagePath := filepath.Join(dir, file)

	page := newPageMapping(m)
	setBlockStyleDeep(page)
	pageDoc := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{
		{Kind: yaml.SequenceNode, Content: []*yaml.Node{page}},
	}}
	if err := os.WriteFile(pagePath, marshalDocument(pageDoc), 0o644); err != nil {
		if isWriteBlockedError(err) {
			return &editorPermissionError{pagePath}
		}
		return err
	}

	include := newMappingNode()
	addPair(include, "$include", scalarNode(file))
	pages.Content = append(pages.Content, include)

	if err := a.writeConfigCandidate(mainPath, marshalDocument(mainDoc)); err != nil {
		os.Remove(pagePath)
		return err
	}
	return nil
}

func uniquePageFileName(dir, title string) string {
	base := titleToSlug(title)
	base = pageFileNamePattern.ReplaceAllString(base, "")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "page"
	}

	name := base + ".yml"
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(dir, name)); os.IsNotExist(err) {
			return name
		}
		name = fmt.Sprintf("%s-%d.yml", base, i)
	}
}

func (a *application) writeConfigCandidate(path string, candidate []byte) error {
	return a.writeConfigCandidates(map[string][]byte{path: candidate})
}

func (a *application) writeConfigCandidates(candidates map[string][]byte) error {
	if len(candidates) == 0 {
		return nil
	}

	originals := map[string][]byte{}
	perms := map[string]os.FileMode{}

	for path, candidate := range candidates {
		if err := checkIncludesStayInConfigDir(candidate, filepath.Dir(a.configPath)); err != nil {
			return &editorValidationError{err: err, field: "$include"}
		}

		perms[path] = os.FileMode(0o644)
		if info, err := os.Stat(path); err == nil {
			perms[path] = info.Mode()
		}

		// Without the original there is nothing to roll back to, so refuse rather than risk truncating.
		original, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		originals[path] = original
	}

	var written []string
	rollback := func() {
		for _, path := range written {
			os.WriteFile(path, originals[path], perms[path])
		}
	}

	for path, candidate := range candidates {
		if err := os.WriteFile(path, candidate, perms[path]); err != nil {
			rollback()
			if isWriteBlockedError(err) {
				return &editorPermissionError{path}
			}
			return err
		}
		written = append(written, path)
	}

	merged, _, err := parseYAMLIncludes(a.configPath)
	if err == nil {
		_, err = newConfigFromYAML(merged)
	}
	if err != nil {
		rollback()
		return &editorValidationError{err: err}
	}

	return nil
}

func (a *application) writeEditorDocs(docs *editorDocs) error {
	candidates := map[string][]byte{}

	for path, doc := range docs.docs {
		root := documentRoot(doc)
		if root.Kind == 0 {
			continue
		}
		if path != docs.pagePath {
			setBlockStyleDeep(root)
		}

		candidate := marshalDocument(doc)
		if root.Kind == yaml.SequenceNode && len(root.Content) == 0 {
			candidate = nil
		}
		if bytes.Equal(candidate, docs.original[path]) {
			continue
		}
		candidates[path] = candidate
	}

	return a.writeConfigCandidates(candidates)
}

// Second line of defence behind rejectIncludeDirective, covering anything the editor writes.
func checkIncludesStayInConfigDir(candidate []byte, dir string) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	for _, match := range configIncludePattern.FindAllSubmatch(candidate, -1) {
		target := strings.TrimSpace(string(match[2]))

		path := target
		if !filepath.IsAbs(path) {
			path = filepath.Join(absDir, path)
		}

		abs, err := filepath.Abs(path)
		if err != nil || !strings.HasPrefix(abs, absDir+string(filepath.Separator)) {
			return fmt.Errorf("include %q is outside the config directory", target)
		}
	}

	return nil
}

type editorDocs struct {
	docs     map[string]*yaml.Node
	original map[string][]byte
	pagePath string
}

func newEditorDocs() *editorDocs {
	return &editorDocs{docs: map[string]*yaml.Node{}, original: map[string][]byte{}}
}

func (d *editorDocs) track(path string, doc *yaml.Node) {
	path = filepath.Clean(path)
	if _, ok := d.docs[path]; ok {
		return
	}
	d.docs[path] = doc
	if b, err := os.ReadFile(path); err == nil {
		d.original[path] = b
	}
}

func (d *editorDocs) load(ownerPath, file string) (string, *yaml.Node, error) {
	path := file
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(ownerPath), file)
	}
	path = filepath.Clean(path)

	if doc, ok := d.docs[path]; ok {
		return path, doc, nil
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return "", nil, err
	}

	d.docs[path] = &doc
	d.original[path] = b
	return path, &doc, nil
}

type widgetSlot struct {
	node      *yaml.Node
	ownerPath string
	seq       *yaml.Node
	index     int
	outerSeq  *yaml.Node
	outerIdx  int
}

func expandWidgetSlots(seq *yaml.Node, ownerPath string, docs *editorDocs, depth int) []widgetSlot {
	var slots []widgetSlot
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return slots
	}

	for i, item := range seq.Content {
		raw := widgetSlot{node: item, ownerPath: ownerPath, seq: seq, index: i, outerSeq: seq, outerIdx: i}

		include := includeTarget(item)
		if include == "" || depth >= CONFIG_INCLUDE_RECURSION_DEPTH_LIMIT {
			slots = append(slots, raw)
			continue
		}

		includePath, includeDoc, err := docs.load(ownerPath, include)
		if err != nil {
			slots = append(slots, raw)
			continue
		}
		includeRoot := documentRoot(includeDoc)
		if includeRoot.Kind == 0 || includeRoot.Tag == "!!null" {
			continue
		}
		if includeRoot.Kind != yaml.SequenceNode {
			slots = append(slots, raw)
			continue
		}

		for _, s := range expandWidgetSlots(includeRoot, includePath, docs, depth+1) {
			if s.seq == includeRoot && s.index == 0 {
				s.outerSeq, s.outerIdx = seq, i
			}
			slots = append(slots, s)
		}
	}

	return slots
}

func widgetInsertPoint(slots []widgetSlot, seq *yaml.Node, index int) (*yaml.Node, int) {
	if index < 0 {
		index = 0
	}
	if index >= len(slots) {
		return seq, len(seq.Content)
	}
	if s := slots[index]; s.index == 0 {
		return s.outerSeq, s.outerIdx
	}
	return slots[index].seq, slots[index].index
}

func resolvePageNode(mainDoc *yaml.Node, mainPath string, idx int) (string, *yaml.Node, *yaml.Node, error) {
	pages := getMappingValue(documentRoot(mainDoc), "pages")
	if pages == nil || pages.Kind != yaml.SequenceNode || !validIndex(pages.Content, idx) {
		return "", nil, nil, fmt.Errorf("page %d out of range", idx)
	}

	item := pages.Content[idx]
	if inc := includeTarget(item); inc != "" {
		path := inc
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(mainPath), path)
		}
		doc, err := loadYAMLDocument(path)
		if err != nil {
			return "", nil, nil, err
		}
		return path, doc, firstMapping(documentRoot(doc)), nil
	}

	return mainPath, mainDoc, item, nil
}

func loadYAMLDocument(path string) (*yaml.Node, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	return doc
}

func firstMapping(root *yaml.Node) *yaml.Node {
	if root.Kind == yaml.SequenceNode && len(root.Content) > 0 {
		return root.Content[0]
	}
	return root
}

func includeTarget(item *yaml.Node) string {
	if item.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(item.Content); i += 2 {
		if k := item.Content[i].Value; k == "$include" || k == "!include" {
			return strings.TrimSpace(item.Content[i+1].Value)
		}
	}
	return ""
}

func getMappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func setMappingKey(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = value
			return
		}
	}
	addPair(m, key, value)
}

func removeMappingKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

func columnWidgets(columns *yaml.Node, index int) *yaml.Node {
	if !validIndex(columns.Content, index) {
		return nil
	}
	widgets := getMappingValue(columns.Content[index], "widgets")
	if widgets == nil {
		widgets = sequenceNode()
		addPair(columns.Content[index], "widgets", widgets)
	}
	return widgets
}

func buildWidgetNode(widgetType string, fields map[string]any, rawFields map[string]string) (*yaml.Node, error) {
	if widgetType == "" {
		return nil, fmt.Errorf("widget type is required")
	}
	node := newMappingNode()
	addPair(node, "type", scalarNode(widgetType))
	return node, applyFieldsToNode(node, fields, rawFields)
}

func editWidgetNode(node *yaml.Node, fields map[string]any, rawFields map[string]string) error {
	return applyFieldsToNode(node, fields, rawFields)
}

func applyFieldsToNode(node *yaml.Node, fields map[string]any, rawFields map[string]string) error {
	for _, k := range sortedKeys(fields) {
		if isEmptyValue(fields[k]) {
			removeMappingKey(node, k)
			continue
		}
		value := valueNode(fields[k])
		if err := rejectIncludeDirective(k, value); err != nil {
			return err
		}
		setMappingKey(node, k, value)
	}

	for _, k := range sortedStringKeys(rawFields) {
		if strings.TrimSpace(rawFields[k]) == "" {
			removeMappingKey(node, k)
			continue
		}
		parsed, err := parseYAMLValue(rawFields[k])
		if err != nil {
			return fmt.Errorf("field %s: %w", k, err)
		}
		if err := rejectIncludeDirective(k, parsed); err != nil {
			return err
		}
		setMappingKey(node, k, parsed)
	}

	return nil
}

func applyStylingSection(root *yaml.Node, key string, fields map[string]any) {
	if fields == nil {
		return
	}
	section := getMappingValue(root, key)
	if section == nil {
		section = newMappingNode()
		setMappingKey(root, key, section)
	}
	for _, k := range sortedKeys(fields) {
		v := fields[k]
		if isEmptyValue(v) || v == false || v == float64(0) {
			removeMappingKey(section, k)
			continue
		}
		setMappingKey(section, k, valueNode(v))
	}
	if len(section.Content) == 0 {
		removeMappingKey(root, key)
	}
}

func applyThemePreset(root *yaml.Node, key string, fields map[string]any) {
	theme := getMappingValue(root, "theme")
	if theme == nil {
		theme = newMappingNode()
		setMappingKey(root, "theme", theme)
	}
	presets := getMappingValue(theme, "presets")
	if presets == nil {
		presets = newMappingNode()
		setMappingKey(theme, "presets", presets)
	}
	preset := getMappingValue(presets, key)
	if preset == nil {
		preset = newMappingNode()
		setMappingKey(presets, key, preset)
	}
	for _, k := range sortedKeys(fields) {
		v := fields[k]
		if isEmptyValue(v) || v == false || v == float64(0) {
			removeMappingKey(preset, k)
			continue
		}
		setMappingKey(preset, k, valueNode(v))
	}
}

func removeThemePreset(root *yaml.Node, key string) {
	theme := getMappingValue(root, "theme")
	if theme == nil {
		return
	}
	presets := getMappingValue(theme, "presets")
	if presets == nil {
		return
	}
	removeMappingKey(presets, key)
	if len(presets.Content) == 0 {
		removeMappingKey(theme, "presets")
	}
}

func resetBaseTheme(root *yaml.Node) {
	theme := getMappingValue(root, "theme")
	if theme == nil {
		return
	}
	kept := theme.Content[:0:0]
	for i := 0; i+1 < len(theme.Content); i += 2 {
		if theme.Content[i+1].Kind == yaml.ScalarNode {
			continue
		}
		kept = append(kept, theme.Content[i], theme.Content[i+1])
	}
	theme.Content = kept
	if len(theme.Content) == 0 {
		removeMappingKey(root, "theme")
	}
}

func parseYAMLValue(text string) (*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return scalarNode(""), nil
	}
	return doc.Content[0], nil
}

func marshalDocument(doc *yaml.Node) []byte {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	enc.Encode(doc)
	enc.Close()
	return buf.Bytes()
}

func nodeToText(n *yaml.Node) string {
	if n.Kind == yaml.ScalarNode {
		return n.Value
	}
	return strings.TrimRight(string(marshalDocument(n)), "\n")
}

func scalarValue(n *yaml.Node) string {
	if n == nil {
		return ""
	}
	return n.Value
}

func newMappingNode() *yaml.Node { return &yaml.Node{Kind: yaml.MappingNode} }

func sequenceNode() *yaml.Node { return &yaml.Node{Kind: yaml.SequenceNode} }

func scalarNode(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: v} }

func addPair(m *yaml.Node, key string, value *yaml.Node) {
	m.Content = append(m.Content, scalarNode(key), value)
}

func valueNode(v any) *yaml.Node {
	switch x := v.(type) {
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(x)}
	case float64:
		if x == math.Trunc(x) {
			return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.FormatInt(int64(x), 10)}
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!float", Value: strconv.FormatFloat(x, 'f', -1, 64)}
	case string:
		return scalarNode(x)
	case []any:
		seq := sequenceNode()
		for _, item := range x {
			seq.Content = append(seq.Content, valueNode(item))
		}
		return seq
	case map[string]any:
		// Escape hatch for list entries whose sub-field is edited as raw YAML.
		if raw, ok := x["$yaml"].(string); ok && len(x) == 1 {
			if parsed, err := parseYAMLValue(raw); err == nil {
				return parsed
			}
		}
		m := newMappingNode()
		for _, k := range orderedFieldKeys(x) {
			addPair(m, k, valueNode(x[k]))
		}
		return m
	default:
		return scalarNode(fmt.Sprintf("%v", v))
	}
}

func orderedFieldKeys(m map[string]any) []string {
	keys := sortedKeys(m)
	slices.SortStableFunc(keys, func(a, b string) int { return fieldOrderRank(a) - fieldOrderRank(b) })
	return keys
}

func isEmptyValue(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case []any:
		return len(x) == 0
	case map[string]any:
		return len(x) == 0
	default:
		return false
	}
}

func insertNode(nodes []*yaml.Node, index int, node *yaml.Node) []*yaml.Node {
	if index < 0 || index > len(nodes) {
		return append(nodes, node)
	}
	nodes = append(nodes, nil)
	copy(nodes[index+1:], nodes[index:])
	nodes[index] = node
	return nodes
}

func removeNode(nodes []*yaml.Node, index int) []*yaml.Node {
	return append(nodes[:index], nodes[index+1:]...)
}

func validIndex(nodes []*yaml.Node, index int) bool {
	return index >= 0 && index < len(nodes)
}

func pathWritable(path string) bool {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	f.Close()
	return true
}

func orDefault(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
