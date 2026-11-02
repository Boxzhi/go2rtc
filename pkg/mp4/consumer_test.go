package mp4

import (
	"testing"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/stretchr/testify/require"
)

func TestConsumerDropCount(t *testing.T) {
	first := &core.Sender{Drops: 2}
	second := &core.Sender{Drops: 3}
	consumer := &Consumer{
		Connection: core.Connection{
			Senders: []*core.Sender{first, second},
		},
	}

	require.Equal(t, 5, consumer.DropCount())
}
