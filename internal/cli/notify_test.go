package cli

import "testing"

func TestAppleScriptQuote(t *testing.T) {
	cases := map[string]string{
		`hello`:        `"hello"`,
		`say "hi"`:     `"say \"hi\""`,
		`C:\temp\ghpm`: `"C:\\temp\\ghpm"`,
		`2 个更新：fd`:     `"2 个更新：fd"`,
		`a\b "c"`:      `"a\\b \"c\""`,
	}
	for in, want := range cases {
		if got := appleScriptQuote(in); got != want {
			t.Errorf("appleScriptQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
