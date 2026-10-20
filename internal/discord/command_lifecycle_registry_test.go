package discord

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestQA013RegistryLifecycleCoverage(t *testing.T) {
	covered := map[string]bool{}
	for _, name := range strings.Fields("summon play now pause resume previous skip queue clear stop leave remove move shuffle jump replay loop seek autoplay lyrics history help") {
		covered[name] = true
	}
	for _, command := range commands() {
		if !covered[command.CommandName()] {
			t.Fatal("registered command missing lifecycle oracle", command.CommandName())
		}
		delete(covered, command.CommandName())
	}
	if len(covered) != 0 {
		t.Fatal("lifecycle tests contain unregistered commands", covered)
	}
	raw, err := json.Marshal(commands())
	if err != nil {
		t.Fatal(err)
	}
	t.Log("QA013_REGISTRY=" + string(raw))
}
