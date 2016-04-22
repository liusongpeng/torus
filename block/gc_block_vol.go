package block

import (
	"sync"
	"time"

	"golang.org/x/net/context"

	"github.com/coreos/agro"
	"github.com/coreos/agro/blockset"
	"github.com/coreos/agro/gc"
	"github.com/coreos/agro/models"
	"github.com/hashicorp/go-immutable-radix"
)

func init() {
	gc.RegisterGC("blockvol", NewBlockVolGC)
}

type blockvolGC struct {
	mut       sync.Mutex
	srv       *agro.Server
	inodes    gc.INodeFetcher
	trie      *iradix.Tree
	highwater agro.INodeID
	skip      bool
	curRef    agro.INodeRef
}

func NewBlockVolGC(srv *agro.Server, inodes gc.INodeFetcher) (gc.GC, error) {
	return &blockvolGC{
		srv:    srv,
		inodes: inodes,
	}, nil
}

func (b *blockvolGC) getContext() context.Context {
	ctx, _ := context.WithTimeout(context.TODO(), 2*time.Second)
	return b.srv.ExtendContext(ctx)
}

func (b *blockvolGC) PrepVolume(vol *models.Volume) error {
	b.mut.Lock()
	defer b.mut.Unlock()
	t := iradix.New()
	b.skip = false
	if vol.Type != "block" {
		b.skip = true
		return nil
	}
	mds, err := createBlockMetadata(b.srv.MDS, agro.VolumeID(vol.Id))
	if err != nil {
		return err
	}
	b.curRef, err = mds.GetINode()
	if err != nil {
		return err
	}
	if b.curRef.INode <= 1 {
		b.skip = true
		return nil
	}
	inode, err := b.inodes.GetINode(b.getContext(), b.curRef)
	if err != nil {
		return err
	}
	set, err := blockset.UnmarshalFromProto(inode.Blocks, nil)
	if err != nil {
		return err
	}
	tx := t.Txn()
	refs := set.GetAllBlockRefs()
	for _, ref := range refs {
		if ref.IsZero() {
			continue
		}
		if ref.INode > b.highwater {
			b.highwater = ref.INode
		}
		tx.Insert(ref.ToBytes(), true)
	}
	b.trie = tx.Commit()
	return nil
}

func (b *blockvolGC) IsDead(ref agro.BlockRef) bool {
	b.mut.Lock()
	defer b.mut.Unlock()
	if b.skip {
		return false
	}
	if ref.BlockType() == agro.TypeINode {
		if ref.INode < b.curRef.INode {
			return true
		}
		return false
	}
	if ref.INode > b.highwater {
		return false
	}
	if _, ok := b.trie.Get(ref.ToBytes()); ok {
		return false
	}
	clog.Tracef("%s is dead", ref)
	return true
}

func (b *blockvolGC) Clear() {}