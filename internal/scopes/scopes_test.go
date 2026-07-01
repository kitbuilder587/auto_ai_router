package scopes

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAllows(t *testing.T) {
	assert.True(t, Allows(nil, Empty()))
	assert.True(t, Allows([]string{"vsellm"}, From([]string{"vsellm"})))
	assert.True(t, Allows([]string{"vsellm,avito"}, From([]string{"avito"})))
	assert.True(t, Allows([]string{"vsellm"}, All()))
	assert.False(t, Allows([]string{"vsellm"}, Empty()))
	assert.False(t, Allows([]string{"vsellm"}, From([]string{"avito"})))
}

func TestNormalizeList(t *testing.T) {
	assert.Equal(t, []string{"avito", "vsellm"}, NormalizeList([]string{" VSELLM ", "avito,vsellm", ""}))
}
