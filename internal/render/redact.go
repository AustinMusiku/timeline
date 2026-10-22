package render

import "net/url"

// redactURL hides the password in s, if it has one.
func redactURL(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.User == nil {
		return s
	}
	if _, ok := u.User.Password(); !ok {
		return s
	}
	return u.Redacted()
}
