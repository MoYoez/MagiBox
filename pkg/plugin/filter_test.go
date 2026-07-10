package plugin

import "testing"

func TestFilter(t *testing.T) {
	// blacklist: listed plugins are off, everything else on
	SetFilter("blacklist", []string{"echo"})
	if Enabled("echo") {
		t.Fatal("黑名单内插件应被禁用")
	}
	if !Enabled("ping") {
		t.Fatal("黑名单外插件应启用")
	}

	// whitelist: only listed plugins are on
	SetFilter("whitelist", []string{"ping", " playground "})
	if !Enabled("ping") || !Enabled("playground") {
		t.Fatal("白名单内插件应启用")
	}
	if Enabled("echo") {
		t.Fatal("白名单外插件应被禁用")
	}

	// default / unknown mode: blacklist with empty list = all on
	SetFilter("", nil)
	if !Enabled("anything") {
		t.Fatal("默认应全部启用")
	}
}
