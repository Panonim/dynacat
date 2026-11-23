package dynacat

import "testing"

func TestGetNavigationPages(t *testing.T) {
	app := &application{}
	app.Config.Pages = []page{
		{Title: "Home", Slug: "home"},
		{Title: "Hidden Admin", Slug: "hidden-admin", AllowedGroups: []string{"admins"}},
		{Title: "Visible Admin", Slug: "visible-admin", AllowedGroups: []string{"admins"}, ShowWhenRestricted: true},
		{Title: "Visible User Page", Slug: "user-page", AllowedUsers: []string{"alice"}, ShowWhenRestricted: true},
	}

	type want struct {
		slug       string
		locked     bool
		restricted bool
	}

	tests := []struct {
		name string
		user *authenticatedUser
		want []want
	}{
		{
			name: "anonymous sees unrestricted pages plus locked show-when-restricted pages",
			user: nil,
			want: []want{{"home", false, false}, {"visible-admin", true, true}, {"user-page", true, true}},
		},
		{
			name: "group member sees their page unlocked",
			user: &authenticatedUser{Username: "bob", Groups: []string{"admins"}},
			want: []want{{"home", false, false}, {"hidden-admin", false, false}, {"visible-admin", false, true}, {"user-page", true, true}},
		},
		{
			name: "allowed user sees their page unlocked, others locked",
			user: &authenticatedUser{Username: "alice"},
			want: []want{{"home", false, false}, {"visible-admin", true, true}, {"user-page", false, true}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := app.getNavigationPages(tt.user)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d pages, want %d", len(got), len(tt.want))
			}
			for i, w := range tt.want {
				if got[i].Slug != w.slug || got[i].Locked != w.locked || got[i].Restricted != w.restricted {
					t.Errorf("page %d: got (%s, locked=%v, restricted=%v), want (%s, locked=%v, restricted=%v)",
						i, got[i].Slug, got[i].Locked, got[i].Restricted, w.slug, w.locked, w.restricted)
				}
			}
		})
	}
}
