import { logFailure, responseDetail } from "./editor-log.js";

const PD = typeof pageData !== "undefined" ? pageData : window.pageData;
const API = `${PD.baseURL}/api/editor`;

const PICK_FIELD = "[ pick a field ]";
const MAX_CELLS = 4;
const MAX_FIELDS = 200;
const MAX_DEPTH = 4;

const COLORS = [
    "color-highlight",
    "color-primary",
    "color-positive",
    "color-negative",
    "color-subdue",
    "color-paragraph",
    "color-base",
];

const FORMATS = [
    ["raw", "Raw value"],
    ["number", "Number, grouped"],
    ["approx", "Number, short"],
    ["bytes", "Bytes"],
    ["decimals", "Decimals"],
    ["percent", "Percent"],
    ["percent-change", "Percent change"],
    ["date", "Date"],
    ["relative", "Relative time"],
];

const MATH_OPS = [
    ["", "None"],
    ["add", "Add"],
    ["sub", "Subtract"],
    ["mul", "Multiply"],
    ["div", "Divide"],
];

const NUMBER_RULE_OPS = [
    ["gt", "greater than"],
    ["lt", "less than"],
    ["ge", "at least"],
    ["le", "at most"],
    ["eq", "equal to"],
];

const COLUMN_OVERFLOWS = [
    ["crop", "Crop to one line"],
    ["wrap", "Wrap over two lines"],
];

const COLUMN_ALIGNS = [
    ["", "Automatic"],
    ["left", "Left"],
    ["center", "Center"],
    ["right", "Right"],
];

const TEXT_RULE_OPS = [
    ["is", "is"],
    ["isnot", "is not"],
];

let ctx = null;
let state = null;
let ui = null;

export async function openCustomAPIEditor(context) {
    ctx = context;
    state = await loadState();
    buildUI();
    renderNetwork();
    renderAll();
    fetchData();
}

function newValue(scope) {
    return {
        src: "json",
        scope: scope || "root",
        path: "",
        op: "",
        by: 2,
        format: "raw",
        digits: 2,
        against: "",
        parse: "unix",
        layout: "DateOnly",
        color: "color-highlight",
        rule: null,
        when: null,
        icon: "",
        iconSide: "left",
        label: "",
        align: "",
        overflow: "crop",
        kind: "value",
    };
}

function newIconColumn() {
    return { kind: "icon", scope: "item", src: "json", path: "", op: "is", than: "true", icon: "", elseIcon: "", label: "" };
}

function newBlock(kind) {
    if (kind === "text") return { kind: "text", text: "Label", color: "color-paragraph", icon: "", iconSide: "left" };
    if (kind === "icon") return { kind: "icon", icon: "si:docker" };
    if (kind === "list") return { kind: "list", src: "json", path: "", limit: 5, sort: "", sortOrder: "desc", columns: [newValue("item")] };
    return { kind: "stat", label: "", icon: "", iconSide: "left", value: newValue() };
}

// Only keys that differ from a fresh block are stored, so the config stays readable.
function compactValue(value) {
    const base = newValue(value.scope);
    const out = {};

    for (const [key, current] of Object.entries(value)) {
        if (JSON.stringify(current) !== JSON.stringify(base[key])) out[key] = current;
    }

    out.path = value.path;
    return out;
}

function compactRows(rows) {
    return rows.map((row) => ({
        blocks: row.blocks.map((block) => {
            if (block.kind === "list") {
                return { ...block, columns: block.columns.map((c) => (c.kind === "icon" ? c : compactValue(c))) };
            }
            return block.kind === "stat" ? { ...block, value: compactValue(block.value) } : block;
        }),
    }));
}

function expandRows(rows) {
    return rows.map((row) => ({
        blocks: (row.blocks || []).map((block) => {
            if (block.kind === "list") {
                const columns = (block.columns || []).map((c) => (c.kind === "icon" ? { ...newIconColumn(), ...c } : { ...newValue("item"), ...c }));
                return { ...newBlock("list"), ...block, columns };
            }
            if (block.kind === "stat") {
                return { ...newBlock("stat"), ...block, value: { ...newValue(block.value?.scope), ...block.value } };
            }
            return { ...newBlock(block.kind), ...block };
        }),
    }));
}

async function loadState() {
    const template = ctx.read("template") || "";
    const stored = ctx.hiddenValues.builder;
    let rows = [];
    let restored = false;

    if (typeof stored === "string" && stored.trim() !== "") {
        try {
            rows = expandRows(JSON.parse(stored).rows || []);
            restored = true;
        } catch {
            rows = [];
        }
    }

    const url = ctx.read("url") || "";

    return {
        url,
        headers: pairsFrom(await readConfig("headers")),
        subrequests: await readSubrequests(),
        netOpen: url.trim() === "",
        rows,
        data: null,
        filter: "",
        collapsed: new Set(),
        opened: null,
        reveal: false,
        rendered: null,
        sel: null,
        dirty: false,
        warn: !restored && template.trim() !== "",
    };
}

async function readConfig(name) {
    const text = ctx.read(name);
    if (!text) return {};

    try {
        return (typeof text === "string" ? await ctx.toValue(text) : text) || {};
    } catch {
        return {};
    }
}

function pairsFrom(map) {
    return Object.entries(map || {}).map(([key, value]) => ({ key, value: String(value ?? "") }));
}

function pairsObject(pairs) {
    const out = {};
    for (const pair of pairs) {
        if (pair.key.trim() !== "") out[pair.key.trim()] = pair.value;
    }
    return out;
}

async function readSubrequests() {
    return Object.entries(await readConfig("subrequests")).map(([name, req]) => ({
        name,
        url: (req && req.url) || "",
        headers: pairsFrom(req && req.headers),
    }));
}

function subrequestsObject() {
    const out = {};
    for (const sub of state.subrequests) {
        if (sub.name.trim() === "") continue;
        const headers = pairsObject(sub.headers);
        out[sub.name.trim()] = Object.keys(headers).length > 0 ? { url: sub.url, headers } : { url: sub.url };
    }
    return out;
}

const iconTrash = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5"><path stroke-linecap="round" stroke-linejoin="round" d="m14.74 9-.346 9m-4.788 0L9.26 9m9.968-3.21c.342.052.682.107 1.022.166m-1.022-.165L18.16 19.673a2.25 2.25 0 0 1-2.244 2.077H8.084a2.25 2.25 0 0 1-2.244-2.077L4.772 5.79m14.456 0a48.108 48.108 0 0 0-3.478-.397m-12 .562c.34-.059.68-.114 1.022-.165m0 0a48.11 48.11 0 0 1 3.478-.397m7.5 0v-.916c0-1.18-.91-2.164-2.09-2.201a51.964 51.964 0 0 0-3.32 0c-1.18.037-2.09 1.022-2.09 2.201v.916m7.5 0a48.667 48.667 0 0 0-7.5 0"/></svg>`;
const iconPlus = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path stroke-linecap="round" d="M12 5v14M5 12h14"/></svg>`;
const iconChevron = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d="m9 5 7 7-7 7"/></svg>`;

let drag = null;
let statusTimer = null;

function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
}

function div(className, text) {
    return el("div", className, text);
}

function button(label, className) {
    const btn = el("button", className, label);
    btn.type = "button";
    return btn;
}

function iconBtn(title, svg, onClick) {
    const btn = button("", "capi-icon-btn");
    btn.title = title;
    btn.innerHTML = svg;
    btn.addEventListener("click", (event) => {
        event.stopPropagation();
        onClick();
    });
    return btn;
}

function iconInput(value, onInput) {
    let timer = null;
    return input(value, "si:docker", (next) => {
        clearTimeout(timer);
        timer = setTimeout(() => onInput(next), 600);
    });
}

function input(value, placeholder, onInput) {
    const field = el("input", "editor-input");
    field.type = "text";
    field.value = value ?? "";
    if (placeholder) field.placeholder = placeholder;
    field.addEventListener("input", () => onInput(field.value));
    return field;
}

function select(options, value, onChange) {
    const field = el("select", "editor-input");
    for (const [id, label] of options) field.append(new Option(label, id));
    field.value = value;
    field.addEventListener("change", () => onChange(field.value));
    return field;
}

const iconElements = new Map();

function iconImg(icon) {
    const { url, autoInvert } = ctx.resolveIcon(icon);
    let img = iconElements.get(url);

    if (!img) {
        img = el("img", `ui-icon${autoInvert ? " flat-icon" : ""}`);
        img.src = url;
        img.alt = "";
        iconElements.set(url, img);
    }

    return img.cloneNode();
}

function labeled(label, control) {
    const wrapper = div("capi-field");
    wrapper.append(div("capi-field-label", label), control);
    return wrapper;
}

function buildUI() {
    const overlay = div("editor-ui capi-editor");

    const topbar = div("capi-topbar");

    const previewToggle = button("Preview", "editor-btn capi-preview-toggle");
    previewToggle.addEventListener("click", togglePreview);

    const refresh = button("Refresh data", "editor-btn");
    refresh.addEventListener("click", fetchData);
    const tools = div("capi-topbar-tools");
    tools.append(previewToggle, refresh);

    const net = div("capi-net");

    const data = div("capi-pane capi-data");
    const palette = div("capi-palette");
    const filter = input(state.filter, "Filter fields", (value) => {
        state.filter = value;
        renderFields();
    });
    const fields = div("capi-fields");
    data.append(net, palette, labeled("Find a field", filter), fields);

    const canvas = div("capi-canvas");
    const inspector = div("capi-pane capi-inspector");
    const body = div("capi-body");
    body.append(data, canvas, inspector);

    const status = div("capi-status");
    const cancel = button("Cancel", "editor-btn");
    cancel.addEventListener("click", requestClose);
    const apply = button("Apply and save", "editor-btn editor-btn-primary");
    apply.addEventListener("click", () => applyChanges(apply));
    const actions = div("capi-topbar-actions");
    actions.append(status, tools, cancel, apply);
    topbar.append(actions);

    overlay.append(topbar, body);
    document.body.append(overlay);

    ui = { overlay, net, previewToggle, palette, fields, canvas, inspector, status };

    dropTarget(canvas, "capi-canvas-drop", (d) => d.type === "block", (d) => {
        state.rows.push({ blocks: [] });
        addBlock(d.kind, state.rows[state.rows.length - 1]);
    });

    renderPalette();
    document.addEventListener("keydown", onKeyDown);
}

function onKeyDown(event) {
    if (event.key === "Escape" && ui) requestClose();
}

function closeEditor() {
    document.removeEventListener("keydown", onKeyDown);
    clearTimeout(statusTimer);
    ui.overlay.remove();
    ui = null;
    state = null;
    drag = null;
}

function requestClose() {
    if (!state.dirty) return closeEditor();
    ctx.confirmAction("Leave the editor without applying your changes?", closeEditor, {
        confirmLabel: "Leave without saving",
        note: "The saved widget stays exactly as it is.",
    });
}

// Routine notes clear themselves after a few seconds, errors stay until something replaces them.
function setStatus(message, level) {
    clearTimeout(statusTimer);
    ui.status.textContent = message || "";
    ui.status.className = `capi-status${level ? ` capi-status-${level}` : ""}`;
    if (message && !level) statusTimer = setTimeout(() => setStatus(""), 4000);
}

function startDrag(element, payload) {
    element.draggable = true;
    element.addEventListener("dragstart", (event) => {
        drag = payload();
        event.dataTransfer.effectAllowed = "copy";
        event.dataTransfer.setData("text/plain", "");
        element.classList.add("capi-dragging");
        ui.overlay.classList.add("capi-drag-active");
    });
    element.addEventListener("dragend", () => {
        drag = null;
        element.classList.remove("capi-dragging");
        ui.overlay.classList.remove("capi-drag-active");
    });
}

function dropTarget(element, className, accepts, onDrop, onHover) {
    const mark = (active) => {
        element.classList.toggle(className, active);
        onHover?.(active);
    };

    element.addEventListener("dragover", (event) => {
        if (!drag || !accepts(drag)) return;
        event.preventDefault();
        event.stopPropagation();
        mark(true);
    });
    element.addEventListener("dragleave", (event) => {
        if (!element.contains(event.relatedTarget)) mark(false);
    });
    element.addEventListener("drop", (event) => {
        if (!drag || !accepts(drag)) return;
        event.preventDefault();
        event.stopPropagation();
        mark(false);
        onDrop(drag);
    });
}

async function postPreview(extra) {
    const url = `${API}/custom-api/preview`;
    const sent = { url: state.url, headers: pairsObject(state.headers), subrequests: subrequestsObject(), ...extra };
    // Header values are the one part of a preview that tends to hold a token.
    const sentSummary = { ...sent, headers: sent.headers && Object.keys(sent.headers) };

    let res;
    try {
        res = await fetch(url, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(sent),
        });
    } catch (err) {
        logFailure("preview never reached the server", { request: `POST ${url}`, sent: sentSummary, error: err });
        throw new Error("The server did not answer");
    }

    const payload = await res.json().catch(() => ({}));
    if (!res.ok) {
        logFailure("preview was rejected", { ...responseDetail("POST", url, res, payload), sent: sentSummary });
        throw new Error(payload.error || "Request failed");
    }

    if (payload.error) {
        logFailure(`preview failed while handling the ${payload.stage || "request"}`, {
            stage: payload.stage,
            subrequest: payload.subrequest,
            message: payload.error,
            hint: payload.hint,
            sent: sentSummary,
        });
    }

    return payload;
}

async function fetchData() {
    setStatus("Fetching data...");

    try {
        const payload = await postPreview({});
        if (payload.error) throw new Error(payload.error);

        state.data = { json: payload.json ?? null, subs: payload.subrequestsJson || {} };
        setStatus(state.url ? "Data loaded" : "Set a request url to load fields", state.url ? "" : "warn");
    } catch (err) {
        state.data = null;
        setStatus(err.message || "Could not fetch data", "error");
    }

    renderFields();
    renderCanvas();
}

function flatten(value, prefix, depth, out) {
    if (out.length >= MAX_FIELDS || depth > MAX_DEPTH || value === null || value === undefined) return out;

    if (Array.isArray(value)) {
        out.push({ path: prefix, label: prefix, value, kind: "array" });
        if (value.length > 0 && typeof value[0] === "object") flatten(value[0], prefix ? `${prefix}.0` : "0", depth + 1, out);
        return out;
    }

    if (typeof value === "object") {
        for (const [key, child] of Object.entries(value)) {
            const path = prefix ? `${prefix}.${key}` : key;
            if (child !== null && typeof child === "object") flatten(child, path, depth + 1, out);
            else out.push({ path, label: path, value: child, kind: typeof child });
            if (out.length >= MAX_FIELDS) break;
        }
        return out;
    }

    out.push({ path: prefix, label: prefix, value, kind: typeof value });
    return out;
}

function sampleText(entry) {
    if (entry.kind === "array") return `${entry.value.length} items`;
    if (typeof entry.value === "number") return entry.value.toLocaleString("en-US");
    const text = String(entry.value);
    return text.length > 40 ? `${text.slice(0, 40)}...` : text;
}

function renderPalette() {
    ui.palette.replaceChildren(div("capi-pane-title", "Add a block"));

    for (const [kind, label] of [["stat", "Stat"], ["text", "Text"], ["list", "Row list"], ["icon", "Icon"]]) {
        const btn = button(label, "editor-btn capi-palette-btn");
        btn.addEventListener("click", () => addBlock(kind));
        startDrag(btn, () => ({ type: "block", kind }));
        ui.palette.append(btn);
    }
}

function renderFields() {
    ui.fields.replaceChildren();

    if (!state.data) {
        ui.fields.append(div("capi-empty", "Set a url above, then hit Refresh data."));
        return;
    }

    const block = selectedBlock();
    if (block && block.kind === "list" && block.path) appendItemGroup(block);

    appendDataGroup("Response", "json", state.data.json);
    for (const [name, json] of Object.entries(state.data.subs)) appendDataGroup(name, name, json);
}

// Show the fields of the first array item, since that is what a row list column binds to.
function appendItemGroup(block) {
    const first = listItems(block)[0];
    if (!first || typeof first !== "object") return;

    const group = div("capi-data-group capi-data-group-item");
    group.append(div("capi-data-group-title", `Fields in each row of ${block.path}`));

    const needle = (state.filter || "").toLowerCase();
    const entries = flatten(first, "", 0, []).filter((entry) => entry.path.toLowerCase().includes(needle));
    if (entries.length === 0) group.append(div("capi-empty", "No fields match."));

    for (const entry of entries) {
        const chip = div("capi-chip");
        chip.append(div("capi-chip-path", entry.label || "(row)"), div("capi-chip-value", sampleText(entry)));
        chip.addEventListener("click", () => bindItemField(block, entry));
        startDrag(chip, () => ({ type: "item-field", block, entry }));
        group.append(chip);
    }

    ui.fields.append(group);
}

function bindItemField(block, entry) {
    const index = state.sel.col === null ? 0 : state.sel.col;
    const column = block.columns[index];
    if (!column) return;

    column.path = entry.path;

    if (column.kind !== "icon") {
        column.format = guessFormat(entry.value);
        if (column.format === "date") column.parse = typeof entry.value === "number" ? "unix" : "rfc3339";
    }
    state.sel = { ...state.sel, col: index };
    state.dirty = true;
    renderAll();
}

function appendDataGroup(title, src, json) {
    const group = div("capi-data-group");
    group.append(div("capi-data-group-title", title));

    const block = selectedBlock();
    const arraysOnly = block && block.kind === "list";
    const needle = (state.filter || "").toLowerCase();
    const entries = flatten(json, "", 0, [])
        .filter((entry) => !arraysOnly || entry.kind === "array")
        .filter((entry) => entry.path.toLowerCase().includes(needle));
    if (entries.length === 0) group.append(div("capi-empty", arraysOnly ? "No arrays in this response." : "No fields match."));

    for (const entry of entries) {
        const chip = div(`capi-chip${entry.kind === "array" ? " capi-chip-array" : ""}`);
        chip.append(div("capi-chip-path", entry.label || "(root)"), div("capi-chip-value", sampleText(entry)));
        chip.addEventListener("click", () => bindField(src, entry, selectedTarget()));
        startDrag(chip, () => ({ type: "field", src, entry }));
        group.append(chip);
    }

    ui.fields.append(group);
}

function addRow() {
    state.rows.push({ blocks: [] });
    state.sel = { r: state.rows.length - 1, c: null, col: null };
    state.dirty = true;
    renderCanvas();
}

function rowForNewBlock(kind) {
    let row = state.sel ? state.rows[state.sel.r] : state.rows[state.rows.length - 1];
    if (row && row.blocks.length === 0) return row;

    // A row list spans the whole widget, so it never shares a row with other blocks.
    if (!row || row.blocks.length >= MAX_CELLS || kind === "list" || row.blocks.some((b) => b.kind === "list")) {
        row = { blocks: [] };
        state.rows.push(row);
    }

    return row;
}

function addBlock(kind, targetRow) {
    if (state.rows.length === 0) state.rows.push({ blocks: [] });

    const fits = targetRow && targetRow.blocks.length < MAX_CELLS
        && (targetRow.blocks.length === 0 || (kind !== "list" && !targetRow.blocks.some((b) => b.kind === "list")));
    const row = fits ? targetRow : rowForNewBlock(kind);

    row.blocks.push(newBlock(kind));
    state.sel = { r: state.rows.indexOf(row), c: row.blocks.length - 1, col: null };
    state.dirty = true;
    renderAll();
}

function selectedBlock() {
    if (!state.sel || state.sel.c === null) return null;
    return state.rows[state.sel.r]?.blocks[state.sel.c] || null;
}

function selectedTarget() {
    const block = selectedBlock();
    return block ? { block, col: state.sel.col } : null;
}

function bindField(src, entry, target) {
    if (!target) return ctx.toast("Pick a block first, then click a field", "negative");

    const { block } = target;

    if (block.kind === "list" && (target.col === null || target.col === undefined)) {
        if (entry.kind !== "array") return ctx.toast("A row list needs an array field", "negative");
        block.src = src;
        block.path = entry.path;
        state.dirty = true;
        return renderAll();
    }

    const value = block.kind === "list" ? block.columns[target.col] : block.kind === "stat" ? block.value : null;
    if (!value) return ctx.toast("This block takes no data", "negative");

    if (value.scope === "item") {
        const prefix = `${block.path}.0.`;
        if (src !== block.src || !entry.path.startsWith(prefix)) {
            return ctx.toast("Pick a field from inside the list array", "negative");
        }
        value.path = entry.path.slice(prefix.length);
    } else {
        value.src = src;
        value.path = entry.path;
    }

    value.format = guessFormat(entry.value);
    if (value.format === "date") value.parse = typeof entry.value === "number" ? "unix" : "rfc3339";
    state.dirty = true;
    renderAll();
}

function guessFormat(sample) {
    if (typeof sample === "number") return Number.isInteger(sample) ? "number" : "decimals";
    if (typeof sample === "string" && /^\d{4}-\d{2}-\d{2}T/.test(sample)) return "date";
    return "raw";
}

let previewApplying = false;
let previewTimer = null;

async function renderTemplate() {
    const payload = await postPreview({ template: generateTemplate() });
    if (payload.error) throw new Error(payload.error);
    return payload.html || "";
}

async function togglePreview() {
    if (state.rendered !== null) {
        clearTimeout(previewTimer);
        state.rendered = null;
        ui.previewToggle.textContent = "Preview";
        return renderCanvas();
    }

    setStatus("Rendering...");

    try {
        const html = await renderTemplate();
        previewApplying = true;
        state.rendered = html;
        ui.previewToggle.textContent = "Back to editing";
        setStatus("");
    } catch (err) {
        setStatus(err.message || "The template could not be rendered", "error");
    }

    renderCanvas();
    previewApplying = false;
}

function renderCanvas() {
    renderCanvasBody();
    if (state.rendered !== null && !previewApplying) schedulePreview();
}

function schedulePreview() {
    clearTimeout(previewTimer);
    previewTimer = setTimeout(refreshPreview, 400);
}

async function refreshPreview() {
    try {
        const html = await renderTemplate();
        previewApplying = true;
        state.rendered = html;
        renderCanvasBody();
        setStatus("");
    } catch (err) {
        setStatus(err.message || "The template could not be rendered", "error");
    } finally {
        previewApplying = false;
    }
}

function renderCanvasBody() {
    ui.overlay.classList.toggle("capi-previewing", state.rendered !== null);
    ui.canvas.replaceChildren();

    const content = div("widget-content");
    const widget = div("widget");
    widget.append(content);
    const preview = div("capi-preview");
    preview.append(widget);

    if (state.rendered !== null) {
        content.innerHTML = state.rendered;
        ui.canvas.append(preview);
        window.dynacatSetupCollapsibleLists?.();
        return;
    }

    if (state.warn) {
        ui.canvas.append(div("capi-warning", "This widget has a hand written template. Applying here replaces it."));
    }

    state.rows.forEach((row, r) => {
        const wrapper = div("capi-row-wrapper");
        const rowEl = div("capi-row");
        if (state.sel && state.sel.r === r && state.sel.c === null) rowEl.classList.add("capi-row-selected");

        wrapper.addEventListener("click", () => {
            state.sel = { r, c: null, col: null };
            renderAll();
        });

        row.blocks.forEach((block, c) => rowEl.append(buildCell(block, row, r, c)));

        if (row.blocks.length === 0) {
            rowEl.append(div("capi-empty", "Drop a block here or pick one on the left."));
        } else if (row.blocks.length < MAX_CELLS && !row.blocks.some((b) => b.kind === "list")) {
            const add = div("capi-cell-add");
            add.innerHTML = iconPlus;
            add.title = "Add a block to this row";
            add.addEventListener("click", (event) => {
                event.stopPropagation();
                addBlock("stat", row);
            });
            rowEl.append(add);
        }

        dropTarget(rowEl, "capi-row-drop", (d) => d.type === "block", (d) => addBlock(d.kind, row));

        const tools = div("capi-row-tools");
        const moveUp = iconBtn("Move row up", iconChevron, () => moveRow(r, -1));
        moveUp.classList.add("capi-row-up");
        moveUp.disabled = r === 0;
        const moveDown = iconBtn("Move row down", iconChevron, () => moveRow(r, 1));
        moveDown.classList.add("capi-row-down");
        moveDown.disabled = r === state.rows.length - 1;

        const removeRow = iconBtn("Remove row", iconTrash, () => {
            state.rows.splice(r, 1);
            state.sel = null;
            state.dirty = true;
            renderAll();
        });
        removeRow.classList.add("capi-icon-btn-danger");
        tools.append(moveUp, moveDown, removeRow);

        wrapper.append(rowEl, tools);
        content.append(wrapper);
    });

    if (state.rows.length === 0) {
        content.append(div("capi-empty", "Pick a block on the left to start this widget."));
    }

    const addRowBtn = button("Add row", "editor-btn capi-add-row");
    addRowBtn.addEventListener("click", addRow);

    ui.canvas.append(preview, addRowBtn);
}

// The selection travels with the row so the inspector keeps showing the same block.
function moveRow(r, step) {
    const target = r + step;
    if (target < 0 || target >= state.rows.length) return;

    [state.rows[r], state.rows[target]] = [state.rows[target], state.rows[r]];
    if (state.sel && state.sel.r === r) state.sel = { ...state.sel, r: target };
    else if (state.sel && state.sel.r === target) state.sel = { ...state.sel, r };
    state.dirty = true;
    renderAll();
}

function buildCell(block, row, r, c) {
    const cell = div(`capi-cell${block.kind === "icon" ? " capi-cell-icon" : ""}`);
    if (state.sel && state.sel.r === r && state.sel.c === c) cell.classList.add("capi-cell-selected");

    cell.addEventListener("click", (event) => {
        event.stopPropagation();
        state.sel = { r, c, col: null };
        renderAll();
    });

    if (block.kind !== "list") {
        cell.append(iconBtn("Remove block", iconTrash, () => {
            row.blocks.splice(c, 1);
            if (row.blocks.length === 0) state.rows.splice(state.rows.indexOf(row), 1);
            state.sel = null;
            state.dirty = true;
            renderAll();
        }));
        cell.firstChild.classList.add("capi-cell-remove");
    }

    cell.append(previewBlock(block, r, c));
    dropTarget(cell, "capi-cell-drop", () => true, (d) => {
        if (d.type === "field") bindField(d.src, d.entry, { block, col: null });
        else addBlock(d.kind, row);
    });

    return cell;
}

function iconRow(node, icon, side) {
    const row = div("flex items-center justify-center gap-7");
    row.append(...(side === "right" ? [node, iconImg(icon)] : [iconImg(icon), node]));
    return row;
}

function previewBlock(block, r, c) {
    const blockIcon = (block.icon || "").trim();

    if (block.kind === "text") {
        const text = div(`size-h4 ${block.color}`, block.text || "Text");
        return blockIcon ? iconRow(text, blockIcon, block.iconSide) : text;
    }

    if (block.kind === "icon") return iconImg(block.icon);

    if (block.kind === "stat") {
        const wrapper = div("text-center");
        const value = previewValue(block.value, null);
        const line = div(`size-h3 ${previewColor(block.value, value.raw)}`, value.text);

        wrapper.append(blockIcon ? iconRow(line, blockIcon, block.iconSide) : line);
        if ((block.label || "").trim()) wrapper.append(div("size-h6 uppercase color-subdue", block.label));
        if (!conditionHolds(block.value, null)) wrapper.classList.add("capi-hidden-by-rule");
        return wrapper;
    }

    const wrapper = div("capi-list-wrapper");
    const body = div("capi-list-body");
    const stack = div("capi-list-stack");
    const list = div("capi-list list list-gap-10");
    const limit = block.limit || 5;

    if (!block.path) wrapper.append(div("capi-list-caption", "Pick an array field on the left, then bind each column"));

    // Fade the other columns so the selected one stands out without extra boxes.
    const selectedCol = state.sel && state.sel.r === r && state.sel.c === c ? state.sel.col : null;
    const muted = (index) => (selectedCol !== null && selectedCol !== index ? " capi-list-cell-muted" : "");
    const columnCells = block.columns.map(() => []);
    const bands = div("capi-col-layer capi-col-bands");
    const hits = div("capi-col-layer capi-col-hits");
    stack.append(bands);

    const markColumn = (index, active) => {
        columnCells[index].forEach((cell) => cell.classList.toggle("capi-list-cell-over", active));
        bands.children[index]?.classList.toggle("capi-col-band-on", active);
    };

    if (block.columns.some((column) => (column.label || "").trim())) {
        const head = div("capi-list-head flex items-center gap-10 size-h6 uppercase color-subdue");
        block.columns.forEach((column, index) => {
            const align = columnAlign(column, index, block.columns.length) + muted(index);
            const cell = column.kind === "icon" ? div(`${align} flex items-center justify-end`) : div(align, column.label || "");
            if (column.kind === "icon") cell.append(el("span", "nowrap", column.label || ""));

            columnCells[index].push(cell);
            head.append(cell);
        });
        stack.append(head);
    }

    sortItems(block, listItems(block)).slice(0, limit).forEach((item) => {
        const line = div("flex items-center gap-10");
        block.columns.forEach((column, index) => {
            const cell = column.kind === "icon" ? previewIconColumn(column, item) : previewColumn(column, item);
            cell.className += ` ${columnAlign(column, index, block.columns.length)}${muted(index)}`;
            columnCells[index].push(cell);

            cell.addEventListener("click", (event) => {
                event.stopPropagation();
                state.sel = { r, c, col: index };
                state.reveal = true;
                renderAll();
            });

            line.append(cell);
        });
        list.append(line);
    });

    block.columns.forEach((column, index) => {
        const align = columnAlign(column, index, block.columns.length);
        bands.append(div(`capi-col-band ${align}`));

        const hit = div(`capi-col-hit ${align}`);
        hit.title = "Drop a field here to replace this column";
        dropTarget(hit, "capi-col-hit-over", (d) => d.type === "field" || d.type === "item-field", (d) => {
            state.sel = { r, c, col: index };
            state.reveal = true;
            if (d.type === "item-field") bindItemField(block, d.entry);
            else bindField(d.src, d.entry, { block, col: index });
        }, (active) => markColumn(index, active));

        hits.append(hit);
    });

    stack.append(list, hits);
    body.append(stack, columnAddZone(block, r, c));
    wrapper.append(body);
    return wrapper;
}

// The counterpart to dropping on a column: this one appends a column instead of replacing one.
function columnAddZone(block, r, c) {
    const zone = div("capi-col-add");
    zone.innerHTML = iconPlus;
    zone.title = "Drop a field here to add a column";

    dropTarget(zone, "capi-col-add-over", (d) => d.type === "field" || d.type === "item-field", (d) => {
        if (d.type === "field" && !columnFieldFits(block, d)) {
            return ctx.toast("Pick a field from inside the list array", "negative");
        }

        block.columns.push(newValue("item"));
        const index = block.columns.length - 1;
        state.sel = { r, c, col: index };
        state.reveal = true;

        if (d.type === "item-field") bindItemField(block, d.entry);
        else bindField(d.src, d.entry, { block, col: index });
    });

    return zone;
}

function columnFieldFits(block, d) {
    return Boolean(block.path) && d.src === block.src && d.entry.path.startsWith(`${block.path}.0.`);
}

function previewColumn(column, item) {
    const value = previewValue(column, item);
    const cell = div(`capi-list-cell ${previewColor(column, value.raw)}`);
    const text = el("span", "", value.text);

    if ((column.icon || "").trim()) {
        cell.classList.add("flex", "items-center", "gap-7");
        cell.append(...(column.iconSide === "right" ? [text, iconImg(column.icon)] : [iconImg(column.icon), text]));
    } else {
        cell.append(text);
    }

    if (!conditionHolds(column, item)) cell.classList.add("capi-hidden-by-rule");
    return cell;
}

function previewIconColumn(column, item) {
    const cell = div("capi-list-cell capi-list-icon");

    if (!column.path || !column.icon) {
        cell.title = "Pick a field and an icon for this column";
        cell.append(el("span", "color-subdue", "?"));
        return cell;
    }

    const raw = getPath(item, column.path);
    const shown = matches({ op: column.op, than: column.than }, raw) ? column.icon : column.elseIcon;
    if ((shown || "").trim()) cell.append(iconImg(shown));
    return cell;
}

function conditionHolds(value, item) {
    if (!value.when) return true;
    return matches(value.when, previewValue(value, item).raw);
}

function matches(rule, raw) {
    if (ruleIsText(rule)) {
        const same = String(raw) === String(rule.than);
        return rule.op === "is" ? same : !same;
    }

    return compare(Number(raw), rule);
}

function sortItems(block, items) {
    if (!block.sort) return items;

    const column = block.columns.find((c) => c.path === block.sort && c.kind !== "icon");
    const numeric = column && isNumericFormat(column.format);
    const direction = block.sortOrder === "asc" ? 1 : -1;

    return [...items].sort((a, b) => {
        const left = getPath(a, block.sort);
        const right = getPath(b, block.sort);
        if (numeric) return (Number(left) - Number(right)) * direction;
        return String(left ?? "").localeCompare(String(right ?? "")) * direction;
    });
}

function sourceJSON(src) {
    if (!state.data) return null;
    return src === "json" ? state.data.json : state.data.subs[src];
}

function listItems(block) {
    const value = getPath(sourceJSON(block.src), block.path);
    return Array.isArray(value) ? value : [];
}

function getPath(source, path) {
    if (source === null || source === undefined) return undefined;
    if (!path) return source;

    let current = source;
    for (const part of path.split(".")) {
        if (current === null || current === undefined) return undefined;
        current = Array.isArray(current) ? current[Number(part)] : current[part];
    }
    return current;
}

function previewValue(value, item) {
    if (!value.path) return { text: PICK_FIELD, raw: null };

    const source = value.scope === "item" ? item : sourceJSON(value.src);
    let raw = getPath(source, value.path);
    if (raw === undefined) return { text: "-", raw: null };

    if (value.op) raw = applyMath(Number(raw), value);
    return { text: formatSample(value, raw, item), raw };
}

function applyMath(number, value) {
    const by = Number(value.by);
    if (value.op === "add") return number + by;
    if (value.op === "sub") return number - by;
    if (value.op === "mul") return number * by;
    if (value.op === "div") return by === 0 ? 0 : number / by;
    return number;
}

function formatSample(value, raw, item) {
    const number = Number(raw);

    switch (value.format) {
        case "number":
            return Number.isFinite(number) ? Math.trunc(number).toLocaleString("en-US") : String(raw);
        case "approx":
            return approxNumber(number);
        case "bytes":
            return formatBytes(number);
        case "decimals":
            return number.toFixed(Number(value.digits));
        case "percent":
            return `${number.toFixed(Number(value.digits))}%`;
        case "percent-change": {
            const source = value.scope === "item" ? item : sourceJSON(value.src);
            const previous = Number(getPath(source, value.against));
            const change = previous === 0 ? 0 : ((number - previous) / previous) * 100;
            return `${change.toFixed(Number(value.digits))}%`;
        }
        case "date":
            return formatDate(value, raw);
        case "relative":
            return relativeTime(toDate(value, raw));
        default:
            return String(raw);
    }
}

function approxNumber(number) {
    if (!Number.isFinite(number)) return "0";
    if (number >= 1000000) return `${Math.round(number / 100000) / 10}m`;
    if (number >= 1000) return `${Math.round(number / 100) / 10}k`;
    return String(Math.trunc(number));
}

function formatBytes(number) {
    if (!Number.isFinite(number)) return "?";
    const units = ["B", "KB", "MB", "GB", "TB", "PB"];
    let value = number;
    let i = 0;
    while (value >= 1024 && i < units.length - 1) {
        value /= 1024;
        i++;
    }
    return `${i === 0 ? Math.round(value) : value.toFixed(1)} ${units[i]}`;
}

function toDate(value, raw) {
    return value.parse === "unix" ? new Date(Number(raw) * 1000) : new Date(String(raw));
}

function formatDate(value, raw) {
    const date = toDate(value, raw);
    if (Number.isNaN(date.getTime())) return "-";
    const iso = date.toISOString();
    return value.layout === "DateTime" ? iso.slice(0, 19).replace("T", " ") : iso.slice(0, 10);
}

function relativeTime(date) {
    if (Number.isNaN(date.getTime())) return "-";
    const seconds = Math.max(1, Math.round((Date.now() - date.getTime()) / 1000));
    if (seconds < 60) return `${seconds}s`;
    if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
    if (seconds < 86400) return `${Math.round(seconds / 3600)}h`;
    if (seconds < 2592000) return `${Math.round(seconds / 86400)}d`;
    return `${Math.round(seconds / 2592000)}mo`;
}

function previewColor(value, raw) {
    if (!value.rule || raw === null) return value.color;
    return matches(value.rule, raw) ? value.rule.then : value.rule.else;
}

function compare(number, rule) {
    const than = Number(rule.than);
    if (rule.op === "gt") return number > than;
    if (rule.op === "lt") return number < than;
    if (rule.op === "ge") return number >= than;
    if (rule.op === "le") return number <= than;
    return number === than;
}

function networkSummary() {
    const parts = [state.url.trim() || "No request url"];
    if (state.headers.length > 0) parts.push(`${state.headers.length} headers`);
    if (state.subrequests.length > 0) parts.push(`${state.subrequests.length} subrequests`);
    return parts.join(" - ");
}

function headersEditor(pairs) {
    const wrapper = div("capi-headers");
    wrapper.append(div("capi-field-label", "Headers"));

    pairs.forEach((pair, index) => {
        const row = div("capi-header-row");
        const remove = iconBtn("Remove header", iconTrash, () => {
            pairs.splice(index, 1);
            state.dirty = true;
            renderNetwork();
        });

        // The name and its remove button share a line, the longer value gets the next one.
        row.append(
            input(pair.key, "Authorization", (value) => {
                pair.key = value;
                state.dirty = true;
            }),
            remove,
            input(pair.value, "Bearer ${API_KEY}", (value) => {
                pair.value = value;
                state.dirty = true;
            })
        );
        wrapper.append(row);
    });

    const add = button("Add header", "editor-btn capi-mini-btn");
    add.addEventListener("click", () => {
        pairs.push({ key: "", value: "" });
        state.dirty = true;
        renderNetwork();
    });
    wrapper.append(add);

    return wrapper;
}

function renderNetwork() {
    ui.net.className = `capi-net${state.netOpen ? "" : " capi-net-collapsed"}`;
    ui.net.replaceChildren();

    const toggle = () => {
        state.netOpen = !state.netOpen;
        renderNetwork();
    };

    const chevron = iconBtn(state.netOpen ? "Collapse network" : "Expand network", iconChevron, toggle);
    chevron.classList.add("capi-net-toggle");
    chevron.setAttribute("aria-expanded", String(state.netOpen));

    const head = div("capi-net-head");
    head.append(div("capi-pane-title", "Network"), chevron);
    head.addEventListener("click", toggle);
    ui.net.append(head);

    if (!state.netOpen) return ui.net.append(div("capi-net-summary", networkSummary()));

    const body = div("capi-net-body");
    body.append(
        labeled("Request url", input(state.url, "https://api.example.com/stats", (value) => {
            state.url = value;
            state.dirty = true;
        })),
        headersEditor(state.headers),
        div("capi-field-label", "Subrequests")
    );

    state.subrequests.forEach((sub, index) => {
        const card = div("capi-source");
        const remove = iconBtn("Remove subrequest", iconTrash, () => {
            state.subrequests.splice(index, 1);
            state.dirty = true;
            renderNetwork();
        });

        card.append(withRemove(labeled("Name", input(sub.name, "tags", (value) => {
            sub.name = value;
            state.dirty = true;
        })), remove));

        card.append(
            labeled("Url", input(sub.url, "https://api.example.com/other", (value) => {
                sub.url = value;
                state.dirty = true;
            })),
            headersEditor(sub.headers)
        );
        body.append(card);
    });

    const add = button("Add subrequest", "editor-btn capi-mini-btn");
    add.addEventListener("click", () => {
        state.subrequests.push({ name: "", url: "", headers: [] });
        state.dirty = true;
        renderNetwork();
    });
    body.append(add);

    ui.net.append(body);
}

function renderInspector() {
    ui.inspector.replaceChildren();

    const block = selectedBlock();
    if (!block) {
        ui.inspector.append(div("capi-empty", "Select a block to edit it."));
        return;
    }

    if (block.kind === "text") {
        ui.inspector.append(
            labeled("Text", input(block.text, "Storage", (value) => {
                block.text = value;
                state.dirty = true;
                renderCanvas();
            }))
        );
        ui.inspector.append(...iconFields(block, "Icon - optional", () => {
            state.dirty = true;
            renderAll();
        }));
        ui.inspector.append(labeled("Color", colorSelect(block, "color")));
        return;
    }

    if (block.kind === "icon") {
        ui.inspector.append(
            labeled("Icon", iconInput(block.icon, (value) => {
                block.icon = value;
                state.dirty = true;
                renderCanvas();
            }))
        );
        return;
    }

    if (block.kind === "stat") {
        ui.inspector.append(
            labeled("Label - optional", input(block.label, "Downloads", (value) => {
                block.label = value;
                state.dirty = true;
                renderCanvas();
            }))
        );
        ui.inspector.append(...iconFields(block, "Icon - optional", () => {
            state.dirty = true;
            renderAll();
        }));
        ui.inspector.append(valueEditor(block.value));
        return;
    }

    ui.inspector.append(labeled("Array to repeat", staticText(block.path || "click an array field on the left")));
    ui.inspector.append(
        labeled("Rows to show", numberInput(block.limit, (value) => {
            block.limit = value;
            state.dirty = true;
            renderCanvas();
        }))
    );

    const sortable = block.columns.filter((column) => column.path && column.kind !== "icon");
    if (sortable.length > 0) {
        const options = [["", "Keep the API order"], ...sortable.map((column) => {
            const position = block.columns.indexOf(column) + 1;
            return [column.path, `Column ${position} - ${column.label || column.path}`];
        })];

        ui.inspector.append(
            labeled("Sort rows by", select(options, block.sort || "", (value) => {
                block.sort = value;
                state.dirty = true;
                renderAll();
            }))
        );
    }

    if (block.sort) {
        ui.inspector.append(
            labeled("Direction", select([["desc", "Highest first"], ["asc", "Lowest first"]], block.sortOrder || "desc", (value) => {
                block.sortOrder = value;
                state.dirty = true;
                renderCanvas();
            }))
        );
    }

    block.columns.forEach((column, index) => {
        const key = `${state.sel.r}:${state.sel.c}:${index}`;
        const selected = state.sel.col === index;

        // Selecting a value opens its card without disturbing the ones the user collapsed.
        if (selected && state.reveal) state.opened = key;
        const collapsed = state.collapsed.has(key) && !(selected && state.opened === key);

        const section = div(`capi-column${selected ? " capi-column-selected" : ""}${collapsed ? " capi-column-collapsed" : ""}`);
        section.addEventListener("click", () => {
            state.sel.col = index;
            renderCanvas();
        });

        const toggle = () => {
            if (collapsed) state.collapsed.delete(key);
            else state.collapsed.add(key);
            if (state.opened === key) state.opened = null;
            renderInspector();
        };

        const chevron = iconBtn(collapsed ? "Expand column" : "Collapse column", iconChevron, toggle);
        chevron.classList.add("capi-column-toggle");
        chevron.setAttribute("aria-expanded", String(!collapsed));

        const head = div("capi-column-head");
        head.append(div("capi-column-title", `Column ${index + 1}${column.label ? ` - ${column.label}` : ""}`), chevron);
        head.addEventListener("click", (event) => {
            event.stopPropagation();
            toggle();
        });

        const remove = button("Remove", "editor-btn capi-mini-btn");
        remove.addEventListener("click", () => {
            block.columns.splice(index, 1);
            state.sel.col = null;
            state.dirty = true;
            renderAll();
        });

        const body = div("capi-column-body");
        body.append(column.kind === "icon" ? iconColumnEditor(column, remove) : valueColumnEditor(column, remove));
        section.append(head, body);
        ui.inspector.append(section);

        if (selected && state.reveal) requestAnimationFrame(() => revealColumn(section));
    });

    state.reveal = false;

    const addColumn = button("+ Column", "editor-btn capi-mini-btn");
    addColumn.addEventListener("click", () => {
        block.columns.push(newValue("item"));
        state.sel.col = block.columns.length - 1;
        state.dirty = true;
        renderAll();
    });

    const addIcon = button("+ Icon column", "editor-btn capi-mini-btn");
    addIcon.addEventListener("click", () => {
        block.columns.push(newIconColumn());
        state.sel.col = block.columns.length - 1;
        state.dirty = true;
        renderAll();
    });

    const columnActions = div("capi-column-actions");
    columnActions.append(addColumn, addIcon);
    ui.inspector.append(columnActions);
}

// A card taller than the pane scrolls to its top, a shorter one is only nudged into view.
function revealColumn(section) {
    const pane = ui.inspector.getBoundingClientRect();
    const card = section.getBoundingClientRect();
    const delta = card.height > pane.height || card.top < pane.top
        ? card.top - pane.top - 8
        : Math.max(0, card.bottom - pane.bottom + 8);

    if (delta) ui.inspector.scrollTo({ top: ui.inspector.scrollTop + delta, behavior: "smooth" });
}

// The remove button sits on the first field, so the card needs no heading of its own.
function withRemove(field, remove) {
    field.classList.add("capi-field-with-action");
    field.firstChild.after(remove);
    return field;
}

function valueColumnEditor(column, remove) {
    const wrapper = div("capi-value");

    wrapper.append(withRemove(labeled("Column label - optional", input(column.label, "Name", (value) => {
        column.label = value;
        state.dirty = true;
        renderCanvas();
    })), remove));

    wrapper.append(
        labeled("Text alignment", select(COLUMN_ALIGNS, column.align, (value) => {
            column.align = value;
            state.dirty = true;
            renderCanvas();
        }))
    );

    wrapper.append(
        labeled("Long values", select(COLUMN_OVERFLOWS, column.overflow, (value) => {
            column.overflow = value;
            state.dirty = true;
            renderCanvas();
        }))
    );

    wrapper.append(...iconFields(column, "Icon - optional", () => {
        state.dirty = true;
        renderAll();
    }));

    wrapper.append(valueEditor(column));
    return wrapper;
}

function iconColumnEditor(column, remove) {
    const wrapper = div("capi-value");

    wrapper.append(withRemove(labeled("Column label - optional", input(column.label, "Status", (value) => {
        column.label = value;
        state.dirty = true;
        renderAll();
    })), remove));

    wrapper.append(labeled("Field", staticText(column.path || "click a field on the left")));

    wrapper.append(
        labeled("Show the first icon when the value", select([...TEXT_RULE_OPS, ...NUMBER_RULE_OPS], column.op, (value) => {
            column.op = value;
            state.dirty = true;
            renderCanvas();
        }))
    );

    wrapper.append(
        labeled("Compared with", input(column.than, "true", (value) => {
            column.than = value;
            state.dirty = true;
            renderCanvas();
        }))
    );

    wrapper.append(
        labeled("Icon when it matches", iconInput(column.icon, (value) => {
            column.icon = value;
            state.dirty = true;
            renderCanvas();
        }))
    );

    wrapper.append(
        labeled("Icon otherwise - leave empty to hide it", iconInput(column.elseIcon, (value) => {
            column.elseIcon = value;
            state.dirty = true;
            renderCanvas();
        }))
    );

    return wrapper;
}

function staticText(text) {
    return div("capi-static", text);
}

function numberInput(value, onChange) {
    const field = el("input", "editor-input");
    field.type = "number";
    field.value = value;
    field.addEventListener("input", () => onChange(Number(field.value)));
    return field;
}

function colorSelect(target, key) {
    return select(COLORS.map((c) => [c, c.replace("color-", "")]), target[key], (value) => {
        target[key] = value;
        state.dirty = true;
        renderCanvas();
    });
}

function valueEditor(value) {
    const wrapper = div("capi-value");

    wrapper.append(labeled("Field", staticText(value.path || "click a field on the left")));

    wrapper.append(
        labeled("Math", select(MATH_OPS, value.op, (op) => {
            value.op = op;
            state.dirty = true;
            renderAll();
        }))
    );

    if (value.op) {
        wrapper.append(
            labeled("By", numberInput(value.by, (by) => {
                value.by = by;
                state.dirty = true;
                renderCanvas();
            }))
        );
    }

    wrapper.append(
        labeled("Format", select(FORMATS, value.format, (format) => {
            value.format = format;
            state.dirty = true;
            renderAll();
        }))
    );

    if (["decimals", "percent", "percent-change"].includes(value.format)) {
        wrapper.append(
            labeled("Decimals", numberInput(value.digits, (digits) => {
                value.digits = digits;
                state.dirty = true;
                renderCanvas();
            }))
        );
    }

    if (value.format === "percent-change") {
        wrapper.append(
            labeled("Compared to field", input(value.against, "previous", (path) => {
                value.against = path;
                state.dirty = true;
                renderCanvas();
            }))
        );
    }

    if (value.format === "date" || value.format === "relative") {
        wrapper.append(
            labeled("Source format", select([["unix", "Unix seconds"], ["rfc3339", "RFC3339"]], value.parse, (parse) => {
                value.parse = parse;
                state.dirty = true;
                renderCanvas();
            }))
        );
    }

    if (value.format === "date") {
        wrapper.append(
            labeled("Shown as", select([["DateOnly", "Date"], ["DateTime", "Date and time"]], value.layout, (layout) => {
                value.layout = layout;
                state.dirty = true;
                renderCanvas();
            }))
        );
    }

    const numeric = isNumericFormat(value.format) || Boolean(value.op);
    const ops = numeric ? NUMBER_RULE_OPS : TEXT_RULE_OPS;
    const defaults = numeric
        ? { op: "gt", than: 0, then: "color-positive", else: "color-negative" }
        : { op: "is", than: "true", then: "color-positive", else: "color-negative" };

    if (value.rule && ruleIsText(value.rule) !== !numeric) value.rule = { ...defaults };

    if (!value.rule) wrapper.append(labeled("Color", colorSelect(value, "color")));

    wrapper.append(checkbox("Color by a rule", value.rule !== null, (on) => {
        value.rule = on ? { ...defaults } : null;
        state.dirty = true;
        renderAll();
    }));

    if (value.rule) {
        const rule = value.rule;
        wrapper.append(
            labeled("When the value", select(ops, rule.op, (op) => {
                rule.op = op;
                state.dirty = true;
                renderCanvas();
            }))
        );
        wrapper.append(labeled("Compared with", thresholdInput(rule, numeric)));
        wrapper.append(labeled("Then", colorSelect(rule, "then")));
        wrapper.append(labeled("Otherwise", colorSelect(rule, "else")));
    }

    wrapper.append(checkbox("Show only when it matches", value.when !== null, (on) => {
        value.when = on ? { op: numeric ? "gt" : "is", than: numeric ? 0 : "true" } : null;
        state.dirty = true;
        renderAll();
    }));

    if (value.when) {
        const when = value.when;
        wrapper.append(
            labeled("Show when the value", select(ops, when.op, (op) => {
                when.op = op;
                state.dirty = true;
                renderCanvas();
            }))
        );
        wrapper.append(labeled("Compared with", thresholdInput(when, numeric)));
    }

    return wrapper;
}

function iconFields(target, label, onChange) {
    const fields = [labeled(label, iconInput(target.icon, (value) => {
        target.icon = value;
        onChange();
    }))];

    if ((target.icon || "").trim()) {
        fields.push(labeled("Icon position", select([["left", "Before the value"], ["right", "After the value"]], target.iconSide || "left", (value) => {
            target.iconSide = value;
            onChange();
        })));
    }

    return fields;
}

function thresholdInput(target, numeric) {
    if (numeric) {
        return numberInput(target.than, (value) => {
            target.than = value;
            state.dirty = true;
            renderCanvas();
        });
    }

    return input(String(target.than ?? ""), "true", (value) => {
        target.than = value;
        state.dirty = true;
        renderCanvas();
    });
}

function checkbox(label, checked, onChange) {
    const row = el("label", "capi-check editor-check");
    const box = el("input");
    box.type = "checkbox";
    box.checked = checked;
    box.addEventListener("change", () => onChange(box.checked));
    row.append(box, el("span", "editor-check-label", label));
    return row;
}

function renderAll() {
    renderFields();
    renderCanvas();
    renderInspector();
}

let varCounter = 0;

function isNumericFormat(format) {
    return ["number", "approx", "bytes", "decimals", "percent", "percent-change"].includes(format);
}

function ruleIsText(rule) {
    return rule.op === "is" || rule.op === "isnot";
}

function methodFor(value) {
    if (value.op) return "Float";
    if (value.rule) return ruleIsText(value.rule) ? "String" : "Float";
    if (["number", "approx", "bytes"].includes(value.format)) return "Int";
    if (["decimals", "percent", "percent-change"].includes(value.format)) return "Float";
    return "String";
}

function accessor(value, path, method) {
    if (value.scope === "item") return `$item.${method} "${path}"`;
    if (value.src === "json") return `$.JSON.${method} "${path}"`;
    return `($.Subrequest "${value.src}").JSON.${method} "${path}"`;
}

function floatLiteral(number) {
    return Number.isInteger(Number(number)) ? `${Number(number)}.0` : String(Number(number));
}

function baseExpression(value) {
    const method = methodFor(value);
    let expr = `(${accessor(value, value.path, method)})`;
    if (value.op) expr = `(${value.op} ${expr} ${floatLiteral(value.by)})`;
    return expr;
}

// The whole-number helpers need an int, so a float expression is narrowed first.
function formatExpression(value, base, isFloat) {
    const whole = isFloat ? `(toInt ${base})` : base;

    switch (value.format) {
        case "number":
            return `formatNumber ${whole}`;
        case "approx":
            return `formatApproxNumber ${whole}`;
        case "bytes":
            return `formatBytes ${base}`;
        case "decimals":
            return `printf "%.${value.digits}f" ${base}`;
        case "percent":
            return `printf "%.${value.digits}f%%" ${base}`;
        case "percent-change":
            if (!value.against) return `printf "%.${value.digits}f%%" ${base}`;
            return `printf "%.${value.digits}f%%" (percentChange ${base} (${accessor(value, value.against, "Float")}))`;
        case "date":
            return `formatTime "${value.layout}" (parseTime "${value.parse}" ${base})`;
        default:
            return base;
    }
}

function ruleClass(rule, variable) {
    return `{{ if ${comparison(rule, variable)} }}${rule.then}{{ else }}${rule.else}{{ end }}`;
}

function comparison(rule, variable) {
    if (ruleIsText(rule)) return `${rule.op === "is" ? "eq" : "ne"} ${variable} "${String(rule.than).replace(/"/g, "")}"`;
    return `${rule.op} ${variable} ${floatLiteral(rule.than)}`;
}

// A condition reads its own copy of the field so it never depends on the value pipeline.
function conditionExpr(value, condition) {
    const method = ruleIsText(condition) ? "String" : "Float";
    return comparison(condition, `(${accessor(value, value.path, method)})`);
}

// Returns a value as its class name, the lines to emit before it, and the printed expression.
function valueParts(value) {
    if (!value.path) return { before: [], className: "color-subdue", output: `"${PICK_FIELD}"` };

    const base = baseExpression(value);
    const isFloat = methodFor(value) === "Float";

    if (!value.rule) {
        return { before: [], className: value.color, output: formatExpression(value, base, isFloat) };
    }

    const variable = `$v${varCounter++}`;

    return {
        before: [`{{ ${variable} := ${base} }}`],
        className: ruleClass(value.rule, variable),
        output: formatExpression({ ...value, op: "" }, variable, isFloat),
    };
}

// Wraps markup in a condition when the value is set to show only in some cases.
function guarded(value, indent, lines) {
    if (!value.when || !value.path) return lines;
    return [`${indent}{{ if ${conditionExpr(value, value.when)} }}`, ...lines, `${indent}{{ end }}`];
}

function iconTag(icon) {
    const resolved = ctx.resolveIcon(icon);
    return `<img class="ui-icon${resolved.autoInvert ? " flat-icon" : ""}" src="${resolved.url}" alt="">`;
}

function statMarkup(block, indent) {
    const label = (block.label || "").trim();
    const icon = (block.icon || "").trim();
    const labelLine = label ? [`${indent}  <div class="size-h6 uppercase color-subdue">${escapeText(label)}</div>`] : [];
    const parts = block.value.format === "relative" && block.value.path ? null : valueParts(block.value);

    const valueLine = parts
        ? `${indent}  <div class="size-h3 ${parts.className}">{{ ${parts.output} }}</div>`
        : `${indent}  <div class="size-h3 ${block.value.color}" {{ toRelativeTime (parseTime "${block.value.parse}" (${accessor(block.value, block.value.path, "String")})) }}></div>`;

    const inner = icon
        ? [
            `${indent}  <div class="flex items-center justify-center gap-7">`,
            ...(block.iconSide === "right"
                ? [`${indent}    ${valueLine.trim()}`, `${indent}    ${iconTag(icon)}`]
                : [`${indent}    ${iconTag(icon)}`, `${indent}    ${valueLine.trim()}`]),
            `${indent}  </div>`,
        ]
        : [valueLine];

    return guarded(block.value, indent, [
        ...(parts ? parts.before.map((line) => indent + line) : []),
        `${indent}<div class="text-center">`,
        ...inner,
        ...labelLine,
        `${indent}</div>`,
    ]);
}

function listArray(block) {
    const source = block.src === "json" ? "$.JSON" : `($.Subrequest "${block.src}").JSON`;
    const array = `${source}.Array "${block.path}"`;

    if (!block.sort) return array;

    const column = block.columns.find((c) => c.path === block.sort && c.kind !== "icon");
    const order = block.sortOrder === "asc" ? "asc" : "desc";

    if (column && column.format === "date") return `sortByTime "${block.sort}" "${column.parse}" "${order}" (${array})`;
    if (column && isNumericFormat(column.format)) return `sortByFloat "${block.sort}" "${order}" (${array})`;
    return `sortByString "${block.sort}" "${order}" (${array})`;
}

// Value columns share the row evenly, an icon column keeps the width of its icon.
function columnAlign(column, index, total) {
    if (column.kind === "icon") return "shrink-0 width-ui-icon";

    return `flex-1 min-width-0 ${overflowClass(column)} text-${column.align || edgeAlign(index, total)}`;
}

// A long label spills the way its column faces, so an edge column never spills out of the widget.
function edgeAlign(index, total) {
    if (index === 0) return "left";
    return index === total - 1 ? "right" : "center";
}

// Wrapping is capped at two lines so a long value cannot push the row list out of shape.
function overflowClass(column) {
    return column.overflow === "wrap" ? "text-truncate-2-lines wrap-anywhere" : "text-truncate";
}

function iconColumnMarkup(column, indent, align) {
    const wrap = `${align} flex items-center justify-center`;
    if (!column.path || !column.icon) return [`${indent}<div class="${wrap}"></div>`];

    const match = comparison({ op: column.op, than: column.than }, `(${accessor(column, column.path, ruleIsText({ op: column.op }) ? "String" : "Float")})`);
    const fallback = (column.elseIcon || "").trim() ? iconTag(column.elseIcon) : "";

    return [`${indent}<div class="${wrap}">{{ if ${match} }}${iconTag(column.icon)}{{ else }}${fallback}{{ end }}</div>`];
}

function columnMarkup(column, indent, align) {
    if (column.kind === "icon") return iconColumnMarkup(column, indent, align);

    const icon = (column.icon || "").trim();
    const wrap = icon ? `${align} flex items-center gap-7` : align;

    const withIcon = (inner) => (column.iconSide === "right" ? inner + iconTag(icon) : iconTag(icon) + inner);

    if (column.format === "relative" && column.path) {
        const attrs = `{{ toRelativeTime (parseTime "${column.parse}" (${accessor(column, column.path, "String")})) }}`;
        const line = icon
            ? `${indent}<div class="${wrap}">${withIcon(`<span class="${overflowClass(column)} ${column.color}" ${attrs}></span>`)}</div>`
            : `${indent}<div class="${align} ${column.color}" ${attrs}></div>`;
        return guarded(column, indent, [line]);
    }

    const parts = valueParts(column);
    const before = parts.before.map((line) => `${indent}${line}`);
    const line = icon
        ? `${indent}<div class="${wrap}">${withIcon(`<span class="${overflowClass(column)} ${parts.className}">{{ ${parts.output} }}</span>`)}</div>`
        : `${indent}<div class="${align} ${parts.className}">{{ ${parts.output} }}</div>`;

    return guarded(column, indent, [...before, line]);
}

function listMarkup(block, indent) {
    if (!block.path) return [`${indent}<div class="size-h4 color-subdue">${escapeText(PICK_FIELD)}</div>`];

    const limit = block.limit || 5;
    const labels = block.columns.filter((column) => (column.label || "").trim());
    const lines = [];

    if (labels.length > 0) {
        lines.push(`${indent}<div class="flex items-center gap-10 size-h6 uppercase color-subdue">`);
        block.columns.forEach((column, index) => {
            const align = columnAlign(column, index, block.columns.length);
            const label = escapeText(column.label || "");
            lines.push(column.kind === "icon"
                ? `${indent}  <div class="${align} flex items-center justify-end"><span class="nowrap">${label}</span></div>`
                : `${indent}  <div class="${align}">${label}</div>`);
        });
        lines.push(`${indent}</div>`);
    }

    lines.push(
        `${indent}<ul class="list list-gap-10 collapsible-container" data-collapse-after="${limit}">`,
        `${indent}  {{ range $item := ${listArray(block)} }}`,
        `${indent}  <li class="flex items-center gap-10">`
    );

    block.columns.forEach((column, index) => {
        lines.push(...columnMarkup(column, `${indent}    `, columnAlign(column, index, block.columns.length)));
    });

    lines.push(`${indent}  </li>`, `${indent}  {{ end }}`, `${indent}</ul>`);
    return lines;
}

function blockMarkup(block, indent) {
    if (block.kind === "text") {
        const icon = (block.icon || "").trim();
        if (!icon) return [`${indent}<div class="size-h4 ${block.color}">${escapeText(block.text)}</div>`];

        const text = `<span class="size-h4 ${block.color}">${escapeText(block.text)}</span>`;
        return [
            `${indent}<div class="flex items-center justify-center gap-7">`,
            `${indent}  ${block.iconSide === "right" ? text + iconTag(icon) : iconTag(icon) + text}`,
            `${indent}</div>`,
        ];
    }

    if (block.kind === "icon") return [`${indent}${iconTag(block.icon)}`];
    if (block.kind === "stat") return statMarkup(block, indent);
    return listMarkup(block, indent);
}

function escapeText(text) {
    return String(text ?? "")
        .replace(/&/g, "&amp;")
        .replace(/</g, "&lt;")
        .replace(/>/g, "&gt;")
        .replace(/\{/g, "&#123;");
}

export function generateTemplate() {
    varCounter = 0;

    const rows = state.rows.filter((row) => row.blocks.length > 0);
    const stacked = rows.length > 1;
    const pad = stacked ? "  " : "";
    const lines = [];

    // Stacked rows need their own gap, a bare sequence of divs renders cramped.
    if (stacked) lines.push('<div class="flex flex-column gap-15">');

    for (const row of rows) {
        if (row.blocks.length === 1) {
            lines.push(...blockMarkup(row.blocks[0], pad));
            continue;
        }

        lines.push(`${pad}<div class="flex items-center justify-between text-center gap-10">`);
        for (const block of row.blocks) {
            lines.push(`${pad}  <div class="${block.kind === "icon" ? "shrink-0" : "flex-1"}">`);
            lines.push(...blockMarkup(block, `${pad}    `));
            lines.push(`${pad}  </div>`);
        }
        lines.push(`${pad}</div>`);
    }

    if (stacked) lines.push("</div>");

    return `${lines.join("\n")}\n`;
}

function validate() {
    const problems = [];
    const blocks = state.rows.flatMap((row) => row.blocks);

    if (blocks.length === 0) problems.push("Add at least one block");

    for (const block of blocks) {
        if (block.kind === "text" && !block.text.trim()) problems.push("A text block has no text");
        if (block.kind === "icon" && !block.icon.trim()) problems.push("An icon block has no icon");
        if (block.kind === "stat") {
            if (!block.value.path) problems.push("A stat block has no field");
            if (block.value.format === "percent-change" && !block.value.against) {
                problems.push("A percent change needs a field to compare against");
            }
        }
        if (block.kind === "list") {
            if (!block.path) problems.push("A row list has no array field");
            if (block.columns.length === 0) problems.push("A row list has no columns");
            if (block.columns.some((column) => !column.path)) problems.push("A row list column has no field");
            if (block.columns.some((column) => column.kind === "icon" && !column.icon)) {
                problems.push("An icon column has no icon");
            }
        }
    }

    const names = new Set(["json", ...state.subrequests.map((sub) => sub.name.trim())]);
    const sources = blocks.flatMap((block) => {
        if (block.kind === "stat") return [block.value.src];
        if (block.kind === "list") return [block.src];
        return [];
    });
    if (sources.some((src) => !names.has(src))) problems.push("A block uses a subrequest that no longer exists");

    const needsData = blocks.some((block) => block.kind === "stat" || block.kind === "list");
    if (needsData && !state.url.trim()) problems.push("Set a request url");

    for (const sub of state.subrequests) {
        if (!sub.name.trim() || !sub.url.trim()) problems.push("Every subrequest needs a name and a url");
    }

    const headers = [state.headers, ...state.subrequests.map((sub) => sub.headers)].flat();
    if (headers.some((header) => !header.key.trim())) problems.push("Every header needs a name");

    return [...new Set(problems)];
}

async function applyChanges(applyBtn) {
    const problems = validate();
    if (problems.length > 0) return setStatus(problems[0], "error");

    const template = generateTemplate();
    applyBtn.disabled = true;
    setStatus("Checking the template...");

    try {
        const payload = await postPreview({ template });
        if (payload.error) throw new Error(payload.error);

        ctx.write("url", state.url);
        ctx.write("template", template);

        const headers = pairsObject(state.headers);
        if (Object.keys(headers).length > 0) await ctx.write("headers", headers);
        else ctx.clear("headers");

        if (state.subrequests.length > 0) await ctx.write("subrequests", subrequestsObject());
        else ctx.clear("subrequests");
        ctx.hiddenValues.builder = JSON.stringify({ v: 1, rows: compactRows(state.rows) }).replaceAll("$", "\\u0024");

        await ctx.submit();
        closeEditor();
    } catch (err) {
        setStatus(err.message || "The template could not be rendered", "error");
    } finally {
        applyBtn.disabled = false;
    }
}
