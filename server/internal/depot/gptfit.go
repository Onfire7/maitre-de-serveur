package depot

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

// Decision 221, revised: firmware that did not list a stick written from a
// smaller image. A raw image carries its backup GPT and its protective MBR
// sized for the image; on a bigger stick the UEFI spec wants them at and
// across the disk's end. FitGPT moves them there, as `sgdisk -e` does, so
// strict firmware sees a well-formed disk.

const sector = 512

var gptSignature = []byte("EFI PART")

// blockDev is what FitGPT needs from a block device or image file.
type blockDev interface {
	io.ReaderAt
	io.WriterAt
}

// FitGPT rewrites the GPT on d, whose size is sectors × 512 bytes, so its
// backup header and entries sit at the end and the protective MBR covers
// the whole disk. A disk without a GPT is left alone (false). Partitions
// are not touched.
func FitGPT(d blockDev, sectors int64) (bool, error) {
	primary := make([]byte, sector)
	if _, err := d.ReadAt(primary, sector); err != nil {
		return false, err
	}
	if !bytes.Equal(primary[:8], gptSignature) {
		return false, nil
	}
	le := binary.LittleEndian
	hdrSize := le.Uint32(primary[12:16])
	if hdrSize < 92 || hdrSize > sector {
		return false, fmt.Errorf("GPT header size %d", hdrSize)
	}
	entriesLBA := le.Uint64(primary[72:80])
	count := le.Uint32(primary[80:84])
	size := le.Uint32(primary[84:88])
	entryBytes := int64(count) * int64(size)
	if entryBytes <= 0 || entryBytes > 1<<20 {
		return false, fmt.Errorf("GPT entries %d × %d", count, size)
	}
	entrySectors := (entryBytes + sector - 1) / sector
	last := uint64(sectors - 1)
	backupEntries := last - uint64(entrySectors)
	newLastUsable := backupEntries - 1
	oldAlt := le.Uint64(primary[32:40])
	if oldAlt == last {
		return false, fixPMBR(d, sectors)
	}
	if oldAlt > last {
		return false, errors.New("the image is bigger than the disk")
	}

	entries := make([]byte, entrySectors*sector)
	if _, err := d.ReadAt(entries[:entryBytes], int64(entriesLBA)*sector); err != nil {
		return false, err
	}
	// No partition may reach past the new last usable sector.
	for i := int64(0); i < int64(count); i++ {
		e := entries[i*int64(size):]
		if end := le.Uint64(e[40:48]); end > newLastUsable && !allZero(e[:16]) {
			return false, fmt.Errorf("partition %d ends past the disk", i+1)
		}
	}

	setHeader := func(h []byte, my, alt, entLBA uint64) {
		le.PutUint64(h[24:32], my)
		le.PutUint64(h[32:40], alt)
		le.PutUint64(h[48:56], newLastUsable)
		le.PutUint64(h[72:80], entLBA)
		le.PutUint32(h[16:20], 0)
		le.PutUint32(h[16:20], crc32.ChecksumIEEE(h[:hdrSize]))
	}
	backup := append([]byte(nil), primary...)
	setHeader(backup, last, 1, backupEntries)
	setHeader(primary, 1, last, entriesLBA)

	// Backup first, so a disk pulled out halfway still has a valid primary.
	if _, err := d.WriteAt(entries, int64(backupEntries)*sector); err != nil {
		return false, err
	}
	if _, err := d.WriteAt(backup, int64(last)*sector); err != nil {
		return false, err
	}
	if _, err := d.WriteAt(primary, sector); err != nil {
		return false, err
	}
	// The image's own backup header is now stray data; clear it.
	if _, err := d.WriteAt(make([]byte, sector), int64(oldAlt)*sector); err != nil {
		return false, err
	}
	return true, fixPMBR(d, sectors)
}

// fixPMBR makes the first MBR entry the spec's protective one: type 0xEE
// from LBA 1 across the disk (or 2 TiB), CHS 0x000200 to 0xFFFFFF.
func fixPMBR(d blockDev, sectors int64) error {
	mbr := make([]byte, sector)
	if _, err := d.ReadAt(mbr, 0); err != nil {
		return err
	}
	e := mbr[446:462]
	if e[4] != 0xEE {
		return nil
	}
	size := uint64(sectors - 1)
	if size > 0xFFFFFFFF {
		size = 0xFFFFFFFF
	}
	e[0] = 0
	copy(e[1:4], []byte{0x00, 0x02, 0x00})
	copy(e[5:8], []byte{0xFF, 0xFF, 0xFF})
	binary.LittleEndian.PutUint32(e[8:12], 1)
	binary.LittleEndian.PutUint32(e[12:16], uint32(size))
	_, err := d.WriteAt(mbr, 0)
	return err
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}
