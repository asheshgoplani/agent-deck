package main

import "testing"

func TestPageReferences(t *testing.T) {
	html := `<link href="a.css"><script src="./js/x.js?v=2#frag"></script><img src="/abs.png"><img src="data:image/png;base64,AA">` +
		`<a href="mailto:x@y">m</a><a href="//cdn.example.com/y.js">c</a><video poster="p.jpg"><source src="v.mp4"></video>` +
		`<div style="background:url(bg.png)"></div><img src="sp%20ace.png">`
	got := pageReferences("sub/page.html", []byte(html))
	want := []string{"sub/a.css", "sub/js/x.js", "sub/p.jpg", "sub/v.mp4", "sub/bg.png", "sub/sp ace.png"}
	for _, w := range want {
		if !bundleRefsContain(got, w) {
			t.Errorf("references miss %s: %v", w, got)
		}
	}
	for _, bad := range []string{"abs.png", "/abs.png", "y.js"} {
		if bundleRefsContain(got, bad) {
			t.Errorf("references include %s: %v", bad, got)
		}
	}
	if refs := pageReferences("page.html", []byte(`<img src="../outside.png">`)); len(refs) != 0 {
		t.Errorf("reference escaping the folder kept: %v", refs)
	}
}

func bundleRefsContain(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
