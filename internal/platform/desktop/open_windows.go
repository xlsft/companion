package desktop

// rundll32 rather than cmd /c start, whose parser would take & in a link
// as a command separator.
func opener(link string) (string, []string, error) {
	return "rundll32", []string{"url.dll,FileProtocolHandler", link}, nil
}
