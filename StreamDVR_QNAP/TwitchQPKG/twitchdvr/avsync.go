package main

import (
	"fmt"
	"os"
)

// tsAVDelta measures the PTS offset between the first audio and video
// presentations in a .ts file: (audio_first − video_first) in seconds.
// A positive value means audio starts later in the file than video.
func tsAVDelta(path string) (float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	buf := make([]byte, 188)

	var pmtPID uint16
	foundPMT := false
	videoPID, audioPID := uint16(0), uint16(0)
	gotV, gotA := false, false
	vPTS, aPTS := 0.0, 0.0
	seenPAT, seenPMT := false, false
	pktNo := 0

	for {
		n, rerr := f.Read(buf)
		if n == 0 {
			break
		}
		if n < 188 {
			break
		}
		if buf[0] != 0x47 {
			// not (aligned) TS; this file was made by ffmpeg so it stays aligned.
			continue
		}
		pktNo++
		pid := uint16(buf[1]&0x1f)<<8 | uint16(buf[2])
		afc := (buf[3] >> 4) & 0x3
		if afc == 2 {
			continue // adaptation field only
		}
		po := 4
		if afc == 3 {
			po = 4 + 1 + int(buf[4])
		}

		switch {
		case pid == 0:
			// PAT. Some muxers (ffmpeg included) omit the PUSI bit on the
			// first packet of a section, so rely on pointer_field==0 + table_id.
			ptr := int(buf[po])
			if ptr != 0 {
				continue
			}
			pi := po + 1 // table_id
			if pi+8 > 188 || buf[pi] != 0x00 {
				continue
			}
			secLen := int(buf[pi+1]&0x0f)<<8 | int(buf[pi+2])
			seenPAT = true
			pe := pi + 3 + secLen - 4 // CRC
			if pe > 188 {
				pe = 188
			}
			j := pi + 8
			for j+4 <= pe && !foundPMT {
				prog := uint16(buf[j])<<8 | uint16(buf[j+1])
				pmt := uint16(buf[j+2]&0x1f)<<8 | uint16(buf[j+3])
				if prog != 0 {
					pmtPID = pmt
					foundPMT = true
				}
				j += 4
			}

		case foundPMT && pid == pmtPID:
			ptr := int(buf[po])
			if ptr != 0 {
				continue
			}
			pi := po + 1 // table_id
			if pi+13 > 188 || buf[pi] != 0x02 {
				continue
			}
			secLen := int(buf[pi+1]&0x0f)<<8 | int(buf[pi+2])
			seenPMT = true
			progInfoLen := int(buf[pi+10]&0x0f)<<8 | int(buf[pi+11])
			pe := pi + 3 + secLen - 4 // CRC
			if pe > 188 {
				pe = 188
			}
			j := pi + 12 + progInfoLen
			for j+5 <= pe {
				stype := int(buf[j])
				epid := uint16(buf[j+1]&0x1f)<<8 | uint16(buf[j+2])
				esLen := int(buf[j+3]&0x0f)<<8 | int(buf[j+4])
				switch stype {
				case 0x1b:
					videoPID = epid
				case 0x0f:
					audioPID = epid
				}
				j += 5 + esLen
			}

		case videoPID != 0 && pid == videoPID && !gotV:
			if pts, ok := pesPTS(buf[po:188]); ok {
				vPTS, gotV = pts, true
			}

		case audioPID != 0 && pid == audioPID && !gotA:
			if pts, ok := pesPTS(buf[po:188]); ok {
				aPTS, gotA = pts, true
			}
		}

		if gotV && gotA {
			return (aPTS - vPTS) / 90000, nil
		}
		if pktNo > 600000 {
			break
		}
		_ = rerr
	}
	if !seenPAT || !seenPMT {
		return 0, fmt.Errorf("no PAT/PMT found (%v/%v)", seenPAT, seenPMT)
	}
	return 0, fmt.Errorf("did not capture first PTS for both streams (v=%v a=%v)", gotV, gotA)
}

// pesPTS extracts the PTS (in 90kHz ticks) from the first PES packet of a
// stream. pkt must be the slice [188 bytes] of the raw TS payload (starting at
// the PES start code 00 00 01).
func pesPTS(pkt []byte) (float64, bool) {
	if len(pkt) < 9 {
		return 0, false
	}
	if pkt[0] != 0 || pkt[1] != 0 || pkt[2] != 1 {
		return 0, false
	}
	// byte6: '10' marker + scrambling/priority...
	if pkt[6]&0xc0 != 0x80 {
		return 0, false
	}
	ptsDtsFlags := (pkt[7] >> 6) & 0x3
	if ptsDtsFlags == 0 {
		return 0, false
	}
	h := 9
	if h+5 > len(pkt) {
		return 0, false
	}
	if ptsDtsFlags >= 2 {
		var v uint64
		v |= uint64(pkt[h]>>1&0x07) << 30
		v |= uint64(pkt[h+1]) << 22
		v |= uint64(pkt[h+2]>>1&0x7f) << 15
		v |= uint64(pkt[h+3]) << 7
		v |= uint64(pkt[h+4] >> 1 & 0x7f)
		return float64(v), true
	}
	return 0, false
}
