package full

import (
	"context"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	availability_test "github.com/celestiaorg/celestia-node/share/availability/test"
)

func init() {
	// randomize quadrant fetching, otherwise quadrant sampling is deterministic
	rand.Seed(time.Now().UnixNano())
}

func TestShareAvailableOverMocknet_Full(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	net := availability_test.NewTestDAGNet(ctx, t)
	_, root, err := RandNode(net, 32)
	assert.NoError(t, err)

	nd, err := Node(net)
	assert.NoError(t, err)

	net.ConnectAll()

	err = nd.SharesAvailable(ctx, root)
	assert.NoError(t, err)
}

func TestSharesAvailable_Full(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// RandServiceWithSquare creates a NewShareAvailability inside, so we can test it
	service, dah, err := RandServiceWithSquare(t, 16)
	assert.NoError(t, err)
	err = service.SharesAvailable(ctx, dah)
	assert.NoError(t, err)
}
