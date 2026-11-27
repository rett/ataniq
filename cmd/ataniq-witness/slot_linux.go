package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/durable"
	"ataniq/internal/enrollment"
)

func slotCommand(ctx context.Context, admin cp.Administration, c config, store durable.Config, cmd command, out io.Writer) error {
	read := cp.ReadRequest{Slot: c.Slot}
	rand.Read(read.Challenge[:])
	v, err := admin.Read(ctx, read)
	if err != nil {
		return err
	}
	if v.Current.Slot != c.Slot || v.Current.Epoch != c.Identity.Epoch {
		return enrollmentRefusal("challenged slot or epoch differs from configuration")
	}
	if err := cp.VerifyView(store.Pin, read, v); err != nil {
		return err
	}
	homeID, err := cp.KeyID(store.HomePin)
	if err != nil || homeID != v.Current.HomeKeyID {
		return errIdentity
	}
	if cmd.op == "slot-read" {
		if cmd.archiveOut != "" {
			path, err := transferPath(cmd.archiveOut, c)
			if err != nil {
				return err
			}
			if err := enrollment.WriteHeadArchive(path, v.Current.Head); err != nil {
				if errors.Is(err, os.ErrExist) {
					return enrollmentRefusal("archive-out already exists; choose a new file")
				}
				return errFiles
			}
		}
		_, err := fmt.Fprintln(out, enrollment.FormatArchiveHead(v.Current.Head))
		return err
	}
	m := cp.Mutation{Expected: v.Current}
	rand.Read(m.ID[:])
	var receipt cp.Receipt
	var intent cp.Intent
	var op string
	switch cmd.op {
	case "slot-release":
		op, intent = "release", m
		receipt, err = admin.Release(ctx, m)
	case "slot-retire":
		q := cp.Retirement{Mutation: m, DispositionDigest: fmt.Sprintf("%x", sha256.Sum256([]byte(cmd.asked)))}
		op, intent = "retire", q
		receipt, err = admin.Retire(ctx, q)
	default:
		return cp.ErrRefused
	}
	if err != nil {
		return err
	}
	witnessID, err := cp.KeyID(store.Pin)
	if err != nil {
		return err
	}
	body, err := intent.Body(witnessID)
	if err != nil {
		return err
	}
	if err := cp.MatchReceipt(store.Pin, c.Identity.Epoch, op, body, receipt); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s_receipt_digest=%s asked=%q\n", op, cp.ReceiptDigest(receipt), cmd.asked)
	return err
}
