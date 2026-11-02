package mp4

import (
	"testing"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/stretchr/testify/require"
)

func TestConsumerDropCountPersistsAfterSenderDrain(t *testing.T) {
	sender := core.NewSender(nil, &core.Codec{Name: core.CodecH264})
	for i := 0; i < 4097; i++ {
		sender.Input(&core.Packet{})
	}

	consumer := &Consumer{
		Connection: core.Connection{
			Senders: []*core.Sender{sender},
		},
	}
	require.Equal(t, 1, consumer.DropCount())

	sender.Close()
	// A queued packet drop remains a recording-integrity failure after Stop.
	require.Equal(t, 1, consumer.DropCount())
}
