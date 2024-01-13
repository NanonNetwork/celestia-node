package store

import (
	"context"
	"errors"
	"github.com/celestiaorg/celestia-node/share"
	"github.com/celestiaorg/rsmt2d"
	"sync/atomic"
)

var _ EdsFile = (*closeOnceFile)(nil)

var errFileClosed = errors.New("file closed")

type closeOnceFile struct {
	f      EdsFile
	closed atomic.Bool
}

func CloseOnceFile(f EdsFile) EdsFile {
	return &closeOnceFile{f: f}
}

func (c *closeOnceFile) Close() error {
	if !c.closed.Swap(true) {
		err := c.f.Close()
		// release reference to the file
		c.f = nil
		return err
	}
	return nil
}

func (c *closeOnceFile) Size() int {
	if c.closed.Load() {
		return 0
	}
	return c.f.Size()
}

func (c *closeOnceFile) Share(ctx context.Context, x, y int) (*share.ShareWithProof, error) {
	if c.closed.Load() {
		return nil, errFileClosed
	}
	return c.f.Share(ctx, x, y)
}

func (c *closeOnceFile) AxisHalf(ctx context.Context, axisType rsmt2d.Axis, axisIdx int) ([]share.Share, error) {
	if c.closed.Load() {
		return nil, errFileClosed
	}
	return c.f.AxisHalf(ctx, axisType, axisIdx)
}

func (c *closeOnceFile) Data(ctx context.Context, namespace share.Namespace, rowIdx int) (share.NamespacedRow, error) {
	if c.closed.Load() {
		return share.NamespacedRow{}, errFileClosed
	}
	return c.f.Data(ctx, namespace, rowIdx)
}

func (c *closeOnceFile) EDS(ctx context.Context) (*rsmt2d.ExtendedDataSquare, error) {
	if c.closed.Load() {
		return nil, errFileClosed
	}
	return c.f.EDS(ctx)
}
