package store

import (
	"context"
	"fmt"
	"os"
	"sync"

	"golang.org/x/sync/errgroup"

	"github.com/celestiaorg/celestia-app/pkg/wrapper"
	"github.com/celestiaorg/rsmt2d"

	"github.com/celestiaorg/celestia-node/share"
)

var _ EdsFile = (*OdsFile)(nil)

type OdsFile struct {
	path string
	hdr  *Header
	fl   *os.File

	memPool memPool
}

// OpenOdsFile opens an existing file. File has to be closed after usage.
func OpenOdsFile(path string) (*OdsFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	h, err := ReadHeader(f)
	if err != nil {
		return nil, err
	}

	// TODO(WWondertan): Validate header
	return &OdsFile{
		path: path,
		hdr:  h,
		fl:   f,
	}, nil
}

func CreateOdsFile(path string, eds *rsmt2d.ExtendedDataSquare, memPools memPools) (*OdsFile, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}

	h := &Header{
		shareSize:  uint16(len(eds.GetCell(0, 0))), // TODO: rsmt2d should expose this field
		squareSize: uint16(eds.Width()),
		version:    FileV0,
	}

	if _, err = h.WriteTo(f); err != nil {
		return nil, err
	}

	for i := uint(0); i < eds.Width()/2; i++ {
		for j := uint(0); j < eds.Width()/2; j++ {
			// TODO: Implemented buffered write through io.CopyBuffer
			shr := eds.GetCell(i, j)
			if _, err := f.Write(shr); err != nil {
				return nil, err
			}
		}
	}

	return &OdsFile{
		path:    path,
		fl:      f,
		hdr:     h,
		memPool: memPools.get(int(h.squareSize) / 2),
	}, f.Sync()
}

func (f *OdsFile) Size() int {
	return f.hdr.SquareSize()
}

func (f *OdsFile) Close() error {
	return f.fl.Close()
}

func (f *OdsFile) Header() *Header {
	return f.hdr
}

func (f *OdsFile) AxisHalf(ctx context.Context, axisType rsmt2d.Axis, axisIdx int) ([]share.Share, error) {
	// read axis from file if axis is in the first quadrant
	if axisIdx < f.Size()/2 {
		return f.odsAxisHalf(axisType, axisIdx)
	}

	ods, err := f.readOds(oppositeAxis(axisType))
	if err != nil {
		return nil, err
	}
	defer f.memPool.ods.Put(ods.square)

	return computeAxisHalf(ctx, ods, f.memPool.codec, axisType, axisIdx)
}

func (f *OdsFile) odsAxisHalf(axisType rsmt2d.Axis, axisIdx int) ([]share.Share, error) {
	switch axisType {
	case rsmt2d.Col:
		return f.readCol(axisIdx)
	case rsmt2d.Row:
		return f.readRow(axisIdx)
	}
	return nil, fmt.Errorf("unknown axis")
}

type odsInMemFile struct {
	EdsFile
	axisType rsmt2d.Axis
	square   [][]share.Share
}

func (f *odsInMemFile) Size() int {
	return len(f.square) * 2
}

func (f *odsInMemFile) AxisHalf(_ context.Context, axisType rsmt2d.Axis, axisIdx int) ([]share.Share, error) {
	if axisType != f.axisType {
		return nil, fmt.Errorf("order of shares is not preserved")
	}
	if axisIdx >= f.Size()/2 {
		return nil, fmt.Errorf("index is out of ods bounds")
	}
	return f.square[axisIdx], nil
}

func (f *OdsFile) readOds(axisType rsmt2d.Axis) (*odsInMemFile, error) {
	shrLn := int(f.hdr.shareSize)
	odsLn := int(f.hdr.squareSize) / 2

	buf := f.memPool.halfAxis.Get().([]byte)
	defer f.memPool.halfAxis.Put(buf)

	ods := f.memPool.ods.Get().([][]share.Share)
	for i := 0; i < odsLn; i++ {
		pos := HeaderSize + odsLn*shrLn*i
		if _, err := f.fl.ReadAt(buf, int64(pos)); err != nil {
			return nil, err
		}

		for j := 0; j < odsLn; j++ {
			if axisType == rsmt2d.Row {
				copy(ods[i][j], buf[j*shrLn:(j+1)*shrLn])
			} else {
				copy(ods[j][i], buf[j*shrLn:(j+1)*shrLn])
			}
		}
	}

	return &odsInMemFile{
		axisType: axisType,
		square:   ods,
	}, nil
}

func (f *OdsFile) readRow(idx int) ([]share.Share, error) {
	shrLn := int(f.hdr.shareSize)
	odsLn := int(f.hdr.squareSize) / 2

	shrs := make([]share.Share, odsLn)

	pos := idx * odsLn
	offset := pos*shrLn + HeaderSize

	axsData := make([]byte, odsLn*shrLn)
	if _, err := f.fl.ReadAt(axsData, int64(offset)); err != nil {
		return nil, err
	}

	for i := range shrs {
		shrs[i] = axsData[i*shrLn : (i+1)*shrLn]
	}
	return shrs, nil
}

func (f *OdsFile) readCol(idx int) ([]share.Share, error) {
	shrLn := int(f.hdr.shareSize)
	odsLn := int(f.hdr.squareSize) / 2

	shrs := make([]share.Share, odsLn)

	for i := 0; i < odsLn; i++ {
		pos := idx + i*odsLn
		offset := pos*shrLn + HeaderSize

		shr := make(share.Share, shrLn)
		if _, err := f.fl.ReadAt(shr, int64(offset)); err != nil {
			return nil, err
		}
		shrs[i] = shr
	}
	return shrs, nil
}

func computeAxisHalf(
	ctx context.Context,
	f EdsFile,
	codec Codec,
	axisType rsmt2d.Axis,
	axisIdx int,
) ([]share.Share, error) {
	shares := make([]share.Share, f.Size()/2)

	// extend opposite half of the square while collecting shares for the first half of required axis
	g, ctx := errgroup.WithContext(ctx)
	opposite := oppositeAxis(axisType)
	for i := 0; i < f.Size()/2; i++ {
		i := i
		g.Go(func() error {
			original, err := f.AxisHalf(ctx, opposite, i)
			if err != nil {
				return err
			}

			enc, err := codec.Encoder(f.Size())
			if err != nil {
				return fmt.Errorf("encoder: %w", err)
			}

			shards := make([][]byte, f.Size())
			copy(shards, original)
			//for j := len(original); j < len(shards); j++ {
			//	shards[j] = make([]byte, len(original[0]))
			//}

			//err = enc.Encode(shards)
			//if err != nil {
			//	return fmt.Errorf("encode: %w", err)
			//}

			target := make([]bool, f.Size())
			target[axisIdx] = true

			err = enc.ReconstructSome(shards, target)
			if err != nil {
				return fmt.Errorf("reconstruct some: %w", err)
			}

			shares[i] = shards[axisIdx]
			return nil
		})
	}

	err := g.Wait()
	return shares, err
}

func (f *OdsFile) axis(ctx context.Context, axisType rsmt2d.Axis, axisIdx int) ([]share.Share, error) {
	original, err := f.AxisHalf(ctx, axisType, axisIdx)
	if err != nil {
		return nil, err
	}

	return extendShares(f.memPool.codec, original)
}

func extendShares(codec Codec, original []share.Share) ([]share.Share, error) {
	sqLen := len(original) * 2
	enc, err := codec.Encoder(sqLen)
	if err != nil {
		return nil, fmt.Errorf("encoder: %w", err)
	}

	shares := make([]share.Share, sqLen)
	copy(shares, original)
	for j := len(original); j < len(shares); j++ {
		shares[j] = make([]byte, len(original[0]))
	}

	err = enc.Encode(shares)
	if err != nil {
		return nil, fmt.Errorf("encoder: %w", err)
	}

	return shares, nil
}

func (f *OdsFile) Share(ctx context.Context, x, y int) (*share.ShareWithProof, error) {
	axisType, axisIdx, shrIdx := rsmt2d.Row, y, x
	if x < f.Size()/2 && y >= f.Size()/2 {
		axisType, axisIdx, shrIdx = rsmt2d.Col, x, y
	}
	shares, err := f.axis(ctx, axisType, axisIdx)
	if err != nil {
		return nil, err
	}

	tree := wrapper.NewErasuredNamespacedMerkleTree(uint64(f.Size()/2), uint(axisIdx))
	for _, shr := range shares {
		err := tree.Push(shr)
		if err != nil {
			return nil, err
		}
	}

	proof, err := tree.ProveRange(shrIdx, shrIdx+1)
	if err != nil {
		return nil, err
	}

	return &share.ShareWithProof{
		Share: shares[shrIdx],
		Proof: &proof,
		Axis:  axisType,
	}, nil
}

func (f *OdsFile) Data(ctx context.Context, namespace share.Namespace, rowIdx int) (share.NamespacedRow, error) {
	shares, err := f.axis(ctx, rsmt2d.Row, rowIdx)
	if err != nil {
		return share.NamespacedRow{}, err
	}
	return ndDataFromShares(shares, namespace, rowIdx)
}

func (f *OdsFile) EDS(_ context.Context) (*rsmt2d.ExtendedDataSquare, error) {
	ods, err := f.readOds(rsmt2d.Row)
	if err != nil {
		return nil, err
	}

	shrs := make([]share.Share, 0, len(ods.square)*len(ods.square))
	for _, row := range ods.square {
		shrs = append(shrs, row...)
	}

	treeFn := wrapper.NewConstructor(uint64(f.hdr.squareSize / 2))
	return rsmt2d.ComputeExtendedDataSquare(shrs, share.DefaultRSMT2DCodec(), treeFn)
}

type memPools struct {
	pools map[int]memPool
	codec Codec
}

type memPool struct {
	codec         Codec
	ods, halfAxis *sync.Pool
}

func newMemPools(codec Codec) memPools {
	return memPools{
		pools: make(map[int]memPool),
		codec: codec,
	}
}
func (m memPools) get(size int) memPool {
	if pool, ok := m.pools[size]; ok {
		return pool
	}
	pool := newMemPool(m.codec, size)
	m.pools[size] = pool
	return pool
}

func newMemPool(codec Codec, size int) memPool {
	ods := &sync.Pool{
		New: func() interface{} {
			shrs := make([][]share.Share, size)
			for i := range shrs {
				if shrs[i] == nil {
					shrs[i] = make([]share.Share, size)
					for j := range shrs[i] {
						shrs[i][j] = make(share.Share, share.Size)
					}
				}
			}
			return shrs
		},
	}

	halfAxis := &sync.Pool{
		New: func() interface{} {
			buf := make([]byte, size*share.Size)
			return buf
		},
	}
	return memPool{
		halfAxis: halfAxis,
		ods:      ods,
		codec:    codec,
	}
}

func oppositeAxis(axis rsmt2d.Axis) rsmt2d.Axis {
	if axis == rsmt2d.Col {
		return rsmt2d.Row
	}
	return rsmt2d.Col
}
