package transport

import "testing"

func TestParseKind(t *testing.T) {
	cases := []struct {
		in   string
		want Kind
		err  bool
	}{
		{"stdio", KindStdio, false},
		{"http", KindHTTP, false},
		{"https", KindHTTP, false},
		{"sse", KindSSE, false},
		{"", "", true},
		{"pigeon", "", true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := ParseKind(c.in)
			if c.err {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("got %s want %s", got, c.want)
			}
		})
	}
}
