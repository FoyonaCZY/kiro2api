package access

import "testing"

func TestInitialKeyAndGroups(t *testing.T) {
	s := Open("")
	s.ImportInitialKey("initial-secret")
	s.ImportInitialKey("later-secret")
	keys := s.ListKeys()
	if len(keys) != 1 || keys[0].Name != "默认" || keys[0].Secret != "initial-secret" || keys[0].GroupID != "default" {
		t.Fatalf("%+v", keys)
	}
	if s.Authenticate("initial-secret") == nil || s.Authenticate("later-secret") != nil {
		t.Fatal("auth")
	}
	g, err := s.CreateGroup("备用")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMembers(g.ID, []uint64{2}, nil); err != nil {
		t.Fatal(err)
	}
	def := s.AllowedCredentials("default", []uint64{1, 2, 3})
	if !def[1] || def[2] || !def[3] {
		t.Fatalf("%v", def)
	}
	spare := s.AllowedCredentials(g.ID, []uint64{1, 2, 3})
	if len(spare) != 1 || !spare[2] {
		t.Fatalf("%v", spare)
	}
	if err := s.DeleteGroup("default"); err == nil || err.Error() != "不能删除默认分组" {
		t.Fatal(err)
	}
	if err := s.SetMembers("default", []uint64{1}, []uint64{1}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMembers("default", nil, []uint64{1}); err == nil {
		t.Fatal("must refuse emptying default")
	}
}
