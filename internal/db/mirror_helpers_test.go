package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCanonicalPushScopeIsDeterministicAndSorted(t *testing.T) {
	assert.Empty(t, CanonicalPushScope(nil, nil))
	assert.Empty(t, CanonicalPushScope([]string{}, []string{}))

	forward := CanonicalPushScope([]string{"b", "a"}, []string{"y", "x"})
	reordered := CanonicalPushScope([]string{"a", "b"}, []string{"x", "y"})
	assert.Equal(t, forward, reordered)
	assert.NotEmpty(t, forward)

	assert.NotEqual(t, CanonicalPushScope([]string{"a"}, nil),
		CanonicalPushScope([]string{"a", "b"}, nil),
	)
	assert.NotEqual(t, CanonicalPushScope([]string{"a"}, nil),
		CanonicalPushScope(nil, []string{"a"}),
	)
}

func TestCanonicalPushScope(t *testing.T) {
	assert.Empty(t, CanonicalPushScope(nil, nil))
	assert.Equal(t, CanonicalPushScope([]string{"b", "a"}, nil), CanonicalPushScope([]string{"a", "b"}, nil))
	assert.NotEqual(t, CanonicalPushScope([]string{"a"}, nil), CanonicalPushScope(nil, []string{"a"}))
}
