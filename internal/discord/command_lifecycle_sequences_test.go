package discord

import (
	"strings"
	"testing"
	"testing/synctest"
)

func TestQA013CriticalCommandSequences(t *testing.T) {
	for _, sequence := range []string{
		"skip previous shuffle EOF", "shuffle move remove EOF", "shuffle jump skip EOF",
		"play pause replay skip EOF", "loop skip clear EOF", "stop play EOF play EOF",
		"clear replay pause resume EOF", "leave summon play EOF", "skip previous replay pause resume EOF",
		"shuffle skip move remove play EOF", "seek EOF queue history", "autoplay clear EOF play EOF",
	} {
		t.Run(strings.ReplaceAll(sequence, " ", "_"), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := newLifecycleFixture(t, "playing", nil)
				for _, operation := range strings.Fields(sequence) {
					if operation == "EOF" {
						f.eof()
					} else {
						f.request(operation)
					}
				}
				f.request("play")
				f.eof()
				f.request("stop")
			})
		})
	}
}
