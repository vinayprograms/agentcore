package workflow

import "testing"

// interfaces_test.go covers interfaces.go: the Node interface contract
// (Name, Kind, Children) on every concrete type, kindOf reflection, and
// general tree-walk navigation.

func TestNode_KindAndNameOnEveryType(t *testing.T) {
	t.Run("agent", func(t *testing.T) {
		var n Node = Agent("alpha", "p")
		if n.Name() != "alpha" || n.Kind() != "agent" {
			t.Errorf("got name=%q kind=%q", n.Name(), n.Kind())
		}
		if n.Children() != nil {
			t.Errorf("agent should have no children")
		}
	})
	t.Run("goal", func(t *testing.T) {
		var n Node = Goal("beta", "do something")
		if n.Name() != "beta" || n.Kind() != "goal" {
			t.Errorf("got name=%q kind=%q", n.Name(), n.Kind())
		}
	})
	t.Run("convergence", func(t *testing.T) {
		var n Node = Convergence("gamma", "refine", 3)
		if n.Name() != "gamma" || n.Kind() != "convergence" {
			t.Errorf("got name=%q kind=%q", n.Name(), n.Kind())
		}
	})
	t.Run("sequence", func(t *testing.T) {
		var n Node = Sequence("delta")
		if n.Name() != "delta" || n.Kind() != "sequence" {
			t.Errorf("got name=%q kind=%q", n.Name(), n.Kind())
		}
	})
	t.Run("workflow", func(t *testing.T) {
		var n Node = New("epsilon")
		if n.Name() != "epsilon" || n.Kind() != "workflow" {
			t.Errorf("got name=%q kind=%q", n.Name(), n.Kind())
		}
	})
}

func TestNode_FullTreeWalkByKind(t *testing.T) {
	w := New("root").
		Input(Parameter{Name: "topic"}).
		Add(
			Sequence("first").Steps(
				Goal("plan", "do plan").Using(Agent("scout", "scan")),
				Convergence("polish", "refine", 3).Using(Agent("editor", "polish")),
			),
		)

	counts := make(map[Kind]int)
	var walk func(Node)
	walk = func(n Node) {
		counts[n.Kind()]++
		for _, c := range n.Children() {
			walk(c)
		}
	}
	walk(w)

	want := map[Kind]int{
		"workflow":    1,
		"sequence":    1,
		"goal":        1,
		"convergence": 1,
		"agent":       2,
	}
	for k, n := range want {
		if counts[k] != n {
			t.Errorf("kind %q: walked %d times, want %d", k, counts[k], n)
		}
	}
	if _, ok := counts["parameter"]; ok {
		t.Error("parameters leaked into the navigation tree")
	}
}

func TestNode_FindByKindAndName(t *testing.T) {
	w := New("root").Add(
		Sequence("main").Steps(
			Goal("plan", "do plan"),
			Goal("execute", "do execute").Using(Agent("worker", "do")),
		),
	)

	worker := findFirst(w, "agent", "worker")
	if worker == nil {
		t.Fatal("could not find agent 'worker'")
	}
	if worker.Kind() != "agent" || worker.Name() != "worker" {
		t.Errorf("found wrong node: kind=%q, name=%q", worker.Kind(), worker.Name())
	}

	if missing := findFirst(w, "agent", "ghost"); missing != nil {
		t.Errorf("expected nil for missing node, got %q", missing.Name())
	}
}

func findFirst(root Node, kind Kind, name string) Node {
	if root.Kind() == kind && root.Name() == name {
		return root
	}
	for _, c := range root.Children() {
		if hit := findFirst(c, kind, name); hit != nil {
			return hit
		}
	}
	return nil
}

func TestKindOf_DerefsPointer(t *testing.T) {
	a := Agent("a", "p")
	if got := kindOf(a); got != "agent" {
		t.Errorf("kindOf pointer: %q", got)
	}
}
