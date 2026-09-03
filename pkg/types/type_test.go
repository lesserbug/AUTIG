package types

import "testing"

func TestEmptyLocalOrderPreservesHeadAndPosition(t *testing.T) {
	previous := [32]byte{1, 2, 3}
	order := &LocalOrder{PrevPosition: 9, NewPosition: 9, PrevHead: previous}
	if got := LocalOrderHead(order); got != previous {
		t.Fatal("empty extension changed the hash-chain head")
	}
}

func TestTransitionIdentifiersCommitOnlyProtocolTransitions(t *testing.T) {
	previous := [32]byte{1}
	evidence := [32]byte{2}
	pre := PreStateIdentifier(3, 4, previous, evidence)
	if pre == previous {
		t.Fatal("pre-state identifier did not commit the evidence transition")
	}
	post := PostStateIdentifier(3, 4, pre, &FairOrderFragment{}, nil)
	if post == pre {
		t.Fatal("post-state identifier did not commit the finalization transition")
	}
}
