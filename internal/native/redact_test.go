package native

import "testing"

func TestRedactURL(t *testing.T) {
	cases := []struct {
		name     string
		in, want string
	}{
		{
			"password present",
			"postgres://user:secret@host:5432/db",
			"postgres://user:***@host:5432/db",
		},
		{
			"no userinfo",
			"postgres://host:5432/db",
			"postgres://host:5432/db",
		},
		{
			"userinfo but no password",
			"postgres://user@host:5432/db",
			"postgres://user@host:5432/db",
		},
		{
			"unparseable input left alone",
			"not a url",
			"not a url",
		},
		{
			"password contains special chars survives",
			"postgres://u:p@ss:w0rd!@host/db",
			"postgres://u:***@host/db",
		},
		{
			"https url with api key style password",
			"https://api:sk_live_xyz@example.com/v1/embed",
			"https://api:***@example.com/v1/embed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactURL(tc.in); got != tc.want {
				t.Errorf("redactURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
