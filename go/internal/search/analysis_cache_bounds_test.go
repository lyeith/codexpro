package search

import (
	"strings"
	"testing"
)

func cacheFact(name string) *sourceFacts {
	return &sourceFacts{TextLen: len(name), Declarations: []declarationFact{{Name: name, Kind: "variable", Line: 1}}}
}

func TestFactCacheKeepsOneCurrentVersionAndDetachesValues(t *testing.T) {
	var c factCache
	first := fileIdentity{Abs: "/fixture/a.ts", ModifiedNS: 1}
	original := cacheFact(strings.Repeat("x", 8192)[:8])
	got := c.storeSource(first, original)
	original.Declarations[0].Name = "mutated"
	if got.Declarations[0].Name != "xxxxxxxx" || c.sourceFact(first) != got {
		t.Fatal("cache aliases caller-owned facts")
	}
	c.storeReadable(readableKey{first, 100}, true)
	second := first
	second.ModifiedNS = 2
	fresh := c.storeSource(second, cacheFact("new"))
	if len(c.entries) != 1 || c.sourceFact(first) != nil || c.sourceFact(second) != fresh {
		t.Fatal("historical version retained or current version discarded")
	}
	if _, known := c.readableFact(readableKey{second, 100}); known {
		t.Fatal("readability crossed file identities")
	}
	c.storeReadable(readableKey{second, 100}, false)
	if value, known := c.readableFact(readableKey{second, 100}); !known || value {
		t.Fatal("negative readability fact lost")
	}
	if _, known := c.readableFact(readableKey{second, 101}); known {
		t.Fatal("readability crossed size limits")
	}
}

func TestFactCacheEvictsByRecencyBytesAndCount(t *testing.T) {
	c := factCache{maxEntries: 2, maxBytes: 1 << 20}
	a := fileIdentity{Abs: "/fixture/a.ts"}
	b := fileIdentity{Abs: "/fixture/b.ts"}
	d := fileIdentity{Abs: "/fixture/d.ts"}
	c.storeSource(a, cacheFact("a"))
	c.storeSource(b, cacheFact("b"))
	c.sourceFact(a)
	c.storeSource(d, cacheFact("d"))
	if c.sourceFact(b) != nil || c.sourceFact(a) == nil || c.sourceFact(d) == nil || len(c.entries) != 2 {
		t.Fatal("entry limit did not evict least recently used file")
	}
	c.maxBytes = c.bytes - 1
	c.storeReadable(readableKey{a, 10}, true)
	if c.bytes > c.maxBytes || len(c.entries) != 1 || c.sourceFact(a) == nil {
		t.Fatal("byte limit did not evict least recently used file")
	}
}

func TestFactCacheOversizedSourceIsReturnedButNotRetained(t *testing.T) {
	c := factCache{maxBytes: 1024}
	id := fileIdentity{Abs: "/fixture/huge.ts"}
	name := strings.Repeat("a", 4096)
	result := c.storeSource(id, cacheFact(name))
	if result.Declarations[0].Name != name || c.sourceFact(id) != nil || c.bytes > 1024 {
		t.Fatal("oversized source either changed or bypassed budget")
	}
}

func TestFactCacheConcurrentFileVersionsRemainValid(t *testing.T) {
	var c factCache
	done := make(chan struct{}, 8)
	for worker := 0; worker < 8; worker++ {
		go func(worker int) {
			for i := 0; i < 100; i++ {
				id := fileIdentity{Abs: "/fixture/shared.ts", ModifiedNS: int64(worker*100 + i + 1)}
				c.storeSource(id, cacheFact("safe"))
				if facts := c.sourceFact(id); facts != nil && facts.Declarations[0].Name != "safe" {
					t.Error("invalid concurrent fact")
				}
			}
			done <- struct{}{}
		}(worker)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if len(c.entries) != 1 {
		t.Fatal("concurrent edits retained multiple versions")
	}
}
