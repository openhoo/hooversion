package envutil

import "testing"

func TestGetDistinguishesInheritanceAndIsolation(t *testing.T) {
	t.Setenv("HOOVERSION_TEST_ENV", "parent")
	for _, test := range []struct {
		env  []string
		want string
	}{{nil, "parent"}, {[]string{}, ""}, {[]string{"HOOVERSION_TEST_ENV=first", "HOOVERSION_TEST_ENV=last"}, "last"}, {[]string{"OTHER=child"}, ""}} {
		if got := Get(test.env, "HOOVERSION_TEST_ENV"); got != test.want {
			t.Errorf("Get(%v)=%q, want %q", test.env, got, test.want)
		}
	}
}
