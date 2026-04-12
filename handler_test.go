package tcmutarget

import (
	"testing"

	tcmu "github.com/uiscsi/go-tcmu"
	"github.com/uiscsi/tapesim"
)

// mustOk fails the test if err is non-nil or the response status is not GOOD (0x00).
func mustOk(t *testing.T, resp tcmu.SCSIResponse, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status() != 0x00 {
		t.Fatalf("expected GOOD status (0x00), got 0x%02x; sense=%x", resp.Status(), resp.SenseBuffer())
	}
}

// mustCheckCondition fails the test if err is non-nil or the response status is not CHECK CONDITION (0x02).
func mustCheckCondition(t *testing.T, resp tcmu.SCSIResponse, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Status() != 0x02 {
		t.Fatalf("expected CHECK CONDITION (0x02), got 0x%02x", resp.Status())
	}
}

// TestTUR_Good verifies that TUR returns GOOD on a fresh media without unit attention.
func TestTUR_Good(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)
	cdb := []byte{0x00, 0, 0, 0, 0, 0}
	cmd := tcmu.NewTestSCSICmd(cdb, nil, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)
}

// TestTUR_UnitAttention verifies that the first TUR after creation with WithUnitAttention
// returns CHECK CONDITION with sense key 0x06 (UNIT ATTENTION) and ASC 0x28.
// The second TUR must return GOOD (unit attention is consume-once).
func TestTUR_UnitAttention(t *testing.T) {
	media := tapesim.NewMedia(1024*1024, tapesim.WithUnitAttention())
	h := NewTapeHandler(media)

	cdb := []byte{0x00, 0, 0, 0, 0, 0}

	// First TUR: should be CHECK CONDITION.
	cmd1 := tcmu.NewTestSCSICmd(cdb, nil, 96)
	resp1, err := h.HandleCommand(cmd1)
	mustCheckCondition(t, resp1, err)

	sense := resp1.SenseBuffer()
	if len(sense) < 14 {
		t.Fatalf("sense buffer too short: %d bytes", len(sense))
	}
	senseKey := sense[2] & 0x0F
	if senseKey != 0x06 {
		t.Fatalf("expected sense key 0x06 (UNIT ATTENTION), got 0x%02x", senseKey)
	}
	asc := sense[12]
	if asc != 0x28 {
		t.Fatalf("expected ASC 0x28 (NOT READY TO READY CHANGE), got 0x%02x", asc)
	}

	// Second TUR: unit attention consumed, should be GOOD.
	cmd2 := tcmu.NewTestSCSICmd(cdb, nil, 96)
	resp2, err := h.HandleCommand(cmd2)
	mustOk(t, resp2, err)
}

// TestInquiry_Standard verifies that standard INQUIRY returns the correct device type,
// Removable bit, and default vendor/product strings.
func TestInquiry_Standard(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	cdb := []byte{0x12, 0, 0, 0, 36, 0}
	dataBuf := make([]byte, 256)
	cmd := tcmu.NewTestSCSICmd(cdb, dataBuf, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	// byte 0: peripheral device type = 0x01 (tape)
	if dataBuf[0] != 0x01 {
		t.Fatalf("expected device type 0x01 (tape), got 0x%02x", dataBuf[0])
	}
	// byte 1: RMB bit (bit 7) must be set for tape
	if dataBuf[1]&0x80 == 0 {
		t.Fatalf("expected Removable bit (byte 1 bit 7) set, got byte 1 = 0x%02x", dataBuf[1])
	}
	// bytes 8-15: vendor ID "UISCSI  "
	vendor := string(dataBuf[8:16])
	if vendor != "UISCSI  " {
		t.Fatalf("expected vendor 'UISCSI  ', got %q", vendor)
	}
	// bytes 16-31: product ID "Virtual Tape    "
	product := string(dataBuf[16:32])
	if product != "Virtual Tape    " {
		t.Fatalf("expected product 'Virtual Tape    ', got %q", product)
	}
}

// TestInquiry_CustomVendor verifies that WithVendor overrides vendor, product, and revision.
func TestInquiry_CustomVendor(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media, WithVendor("MYCO", "MyTape", "0002"))

	cdb := []byte{0x12, 0, 0, 0, 36, 0}
	dataBuf := make([]byte, 256)
	cmd := tcmu.NewTestSCSICmd(cdb, dataBuf, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	vendor := string(dataBuf[8:16])
	if vendor != "MYCO    " {
		t.Fatalf("expected vendor 'MYCO    ', got %q", vendor)
	}
	product := string(dataBuf[16:32])
	if product != "MyTape          " {
		t.Fatalf("expected product 'MyTape          ', got %q", product)
	}
}

// TestInquiry_EVPD_Page00 verifies that EVPD page 0x00 (supported VPD pages) succeeds.
func TestInquiry_EVPD_Page00(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	// INQUIRY CDB with EVPD=1, page code=0x00
	cdb := []byte{0x12, 0x01, 0x00, 0, 255, 0}
	dataBuf := make([]byte, 256)
	cmd := tcmu.NewTestSCSICmd(cdb, dataBuf, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	// byte 0: device type
	if dataBuf[0] != 0x01 {
		t.Fatalf("expected device type 0x01 in VPD response, got 0x%02x", dataBuf[0])
	}
	// page list must contain 0x00
	pageListLen := int(dataBuf[3])
	found := false
	for i := 0; i < pageListLen; i++ {
		if dataBuf[4+i] == 0x00 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("VPD page 0x00 not in supported list: %x", dataBuf[4:4+pageListLen])
	}
}

// TestInquiry_EVPD_Unsupported verifies that an unsupported EVPD page returns ILLEGAL REQUEST.
func TestInquiry_EVPD_Unsupported(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	// INQUIRY CDB with EVPD=1, page code=0x80 (unsupported)
	cdb := []byte{0x12, 0x01, 0x80, 0, 255, 0}
	dataBuf := make([]byte, 256)
	cmd := tcmu.NewTestSCSICmd(cdb, dataBuf, 96)
	resp, err := h.HandleCommand(cmd)
	mustCheckCondition(t, resp, err)
}

// TestReadBlockLimits verifies that READ BLOCK LIMITS returns the correct 6-byte response.
func TestReadBlockLimits(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	cdb := []byte{0x05, 0, 0, 0, 0, 0}
	dataBuf := make([]byte, 6)
	cmd := tcmu.NewTestSCSICmd(cdb, dataBuf, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	// bytes 1-3: maximum block size = 0x100000 (1 MiB)
	maxBS := (int(dataBuf[1]) << 16) | (int(dataBuf[2]) << 8) | int(dataBuf[3])
	if maxBS != 0x100000 {
		t.Fatalf("expected max block size 0x100000, got 0x%06x", maxBS)
	}
	// bytes 4-5: minimum block size = 0x0001
	minBS := (int(dataBuf[4]) << 8) | int(dataBuf[5])
	if minBS != 0x0001 {
		t.Fatalf("expected min block size 0x0001, got 0x%04x", minBS)
	}
}

// TestRewind verifies that REWIND moves the position back to 0 with BOP set.
func TestRewind(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	// Write some data so position > 0.
	data := make([]byte, 512)
	for i := range data {
		data[i] = 0xAB
	}
	_, _ = media.Write(data, false)
	if media.Position() == 0 {
		t.Fatal("expected position > 0 after write")
	}

	// REWIND
	cdb := []byte{0x01, 0, 0, 0, 0, 0}
	cmd := tcmu.NewTestSCSICmd(cdb, nil, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	// READ POSITION to verify BOP and position=0
	posCDB := []byte{0x34, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	posDataBuf := make([]byte, 20)
	posCmd := tcmu.NewTestSCSICmd(posCDB, posDataBuf, 96)
	posResp, posErr := h.HandleCommand(posCmd)
	mustOk(t, posResp, posErr)

	// byte 0 bit 7 = BOP
	if posDataBuf[0]&0x80 == 0 {
		t.Fatalf("expected BOP flag set after rewind, byte 0 = 0x%02x", posDataBuf[0])
	}
	// bytes 4-7 = position (should be 0)
	pos := (uint32(posDataBuf[4]) << 24) | (uint32(posDataBuf[5]) << 16) |
		(uint32(posDataBuf[6]) << 8) | uint32(posDataBuf[7])
	if pos != 0 {
		t.Fatalf("expected position 0 after rewind, got %d", pos)
	}
}

// TestReadPosition_BOP verifies that READ POSITION on fresh media returns BOP flag and position 0.
func TestReadPosition_BOP(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	cdb := []byte{0x34, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	dataBuf := make([]byte, 20)
	cmd := tcmu.NewTestSCSICmd(cdb, dataBuf, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	// byte 0 bit 7 = BOP
	if dataBuf[0]&0x80 == 0 {
		t.Fatalf("expected BOP flag (byte 0 bit 7) set, byte 0 = 0x%02x", dataBuf[0])
	}
	// bytes 4-7 = position (should be 0)
	pos := (uint32(dataBuf[4]) << 24) | (uint32(dataBuf[5]) << 16) |
		(uint32(dataBuf[6]) << 8) | uint32(dataBuf[7])
	if pos != 0 {
		t.Fatalf("expected position 0, got %d", pos)
	}
}

// TestUnknownOpcode verifies that unknown opcodes return NotHandled (CHECK CONDITION).
func TestUnknownOpcode(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	// Opcode 0x60 is in the reserved range (0x60-0x7e) per SPC-4.
	// Using 0x60 as a representative unknown opcode.
	cdb := []byte{0x60, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	cmd := tcmu.NewTestSCSICmd(cdb, nil, 96)
	resp, err := h.HandleCommand(cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// NotHandled returns CHECK CONDITION with sense key 0x05 (ILLEGAL REQUEST)
	// and ASC 0x20 (INVALID COMMAND OPERATION CODE).
	if resp.Status() != 0x02 {
		t.Fatalf("expected CHECK CONDITION for unknown opcode, got status 0x%02x", resp.Status())
	}
}

// TestInjectedError_PreDispatch verifies that injected errors are returned before command logic runs.
func TestInjectedError_PreDispatch(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	// Inject an error for opcode 0x00 (TUR). The injection fires before TUR logic.
	media.InjectError(0x00, 0x03, 0x11, 0x00) // MEDIUM ERROR, Read Error
	cdb := []byte{0x00, 0, 0, 0, 0, 0}
	cmd := tcmu.NewTestSCSICmd(cdb, nil, 96)
	resp, err := h.HandleCommand(cmd)
	mustCheckCondition(t, resp, err)

	sense := resp.SenseBuffer()
	if len(sense) < 14 {
		t.Fatalf("sense buffer too short: %d bytes", len(sense))
	}
	senseKey := sense[2] & 0x0F
	if senseKey != 0x03 {
		t.Fatalf("expected injected sense key 0x03 (MEDIUM ERROR), got 0x%02x", senseKey)
	}

	// After injection consumed, TUR should succeed.
	cmd2 := tcmu.NewTestSCSICmd(cdb, nil, 96)
	resp2, err2 := h.HandleCommand(cmd2)
	mustOk(t, resp2, err2)
}

// ---------------------------------------------------------------------------
// WRITE(6), READ(6), WRITE FILEMARKS(6), SPACE(6) tests
// ---------------------------------------------------------------------------

// writeCmdBuf builds a WRITE(6) test command with the given data.
// The data is pre-loaded into the command's data buffer so cmd.Read returns it.
func writeCmdBuf(data []byte, fixed bool) *tcmu.SCSICmd {
	count := len(data)
	cdb := []byte{0x0A, 0, byte(count >> 16), byte(count >> 8), byte(count), 0}
	if fixed {
		cdb[1] |= 0x01
	}
	buf := make([]byte, len(data))
	copy(buf, data)
	return tcmu.NewTestSCSICmd(cdb, buf, 96)
}

// writeCmdFixed builds a WRITE(6) test command in fixed-block mode.
// blockCount is the number of blocks; data must be blockCount*blockSize bytes.
func writeCmdFixed(blockCount int, data []byte) *tcmu.SCSICmd {
	cdb := []byte{0x0A, 0x01, byte(blockCount >> 16), byte(blockCount >> 8), byte(blockCount), 0}
	buf := make([]byte, len(data))
	copy(buf, data)
	return tcmu.NewTestSCSICmd(cdb, buf, 96)
}

// readCmd builds a READ(6) test command with a pre-allocated result buffer.
func readCmd(count int, fixed, sili bool) (*tcmu.SCSICmd, []byte) {
	cdb := []byte{0x08, 0, byte(count >> 16), byte(count >> 8), byte(count), 0}
	if fixed {
		cdb[1] |= 0x01
	}
	if sili {
		cdb[1] |= 0x02
	}
	buf := make([]byte, count)
	return tcmu.NewTestSCSICmd(cdb, buf, 96), buf
}

// readCmdFixed builds a READ(6) test command in fixed-block mode.
// count is the number of blocks; buf size should be count*blockSize.
func readCmdFixed(blockCount, blockSize int, fixed, sili bool) (*tcmu.SCSICmd, []byte) {
	cdb := []byte{0x08, 0, byte(blockCount >> 16), byte(blockCount >> 8), byte(blockCount), 0}
	if fixed {
		cdb[1] |= 0x01
	}
	if sili {
		cdb[1] |= 0x02
	}
	buf := make([]byte, blockCount*blockSize)
	return tcmu.NewTestSCSICmd(cdb, buf, 96), buf
}

// rewindMedia rewinds via the handler.
func rewindMedia(t *testing.T, h *TapeHandler) {
	t.Helper()
	cdb := []byte{0x01, 0, 0, 0, 0, 0}
	cmd := tcmu.NewTestSCSICmd(cdb, nil, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)
}

// TestWrite_Variable verifies variable-mode WRITE(6) writes the correct bytes.
func TestWrite_Variable(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	data := []byte("hello world")
	cmd := writeCmdBuf(data, false)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	if media.Written() != len(data) {
		t.Fatalf("expected %d bytes written, got %d", len(data), media.Written())
	}
}

// TestWrite_Fixed verifies fixed-mode WRITE(6) multiplies block count by block size.
func TestWrite_Fixed(t *testing.T) {
	const blockSize = 512
	media := tapesim.NewMedia(1024 * 1024)
	media.SetBlockSize(blockSize)
	h := NewTapeHandler(media)

	data := make([]byte, 2*blockSize)
	for i := range data {
		data[i] = 0xAB
	}
	// CDB count = 2 blocks; handler multiplies 2 * 512 = 1024 bytes.
	cmd := writeCmdFixed(2, data)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	if media.Written() != 2*blockSize {
		t.Fatalf("expected %d bytes written (2 blocks * %d), got %d", 2*blockSize, blockSize, media.Written())
	}
}

// TestWrite_EOM verifies that WRITE(6) returns CHECK CONDITION with EOM sense
// when crossing the early-warning threshold (sense key 0x00, EOM bit set).
func TestWrite_EOM(t *testing.T) {
	// Small media: 256 bytes, threshold at 200.
	media := tapesim.NewMedia(256, tapesim.WithEOMThreshold(200))
	h := NewTapeHandler(media)

	data := make([]byte, 250)
	cmd := writeCmdBuf(data, false)
	resp, err := h.HandleCommand(cmd)
	mustCheckCondition(t, resp, err)

	sense := resp.SenseBuffer()
	if len(sense) < 3 {
		t.Fatalf("sense buffer too short: %d", len(sense))
	}
	// Key bits (lower nibble of byte 2) must be 0x00 (NO SENSE + EOM).
	senseKey := sense[2] & 0x0F
	if senseKey != 0x00 {
		t.Fatalf("expected sense key 0x00 for EOM early warning, got 0x%02x", senseKey)
	}
	// EOM bit (bit 6 of byte 2) must be set.
	if sense[2]&0x40 == 0 {
		t.Fatalf("expected EOM bit (byte 2 bit 6) set, byte 2 = 0x%02x", sense[2])
	}
}

// TestWrite_VolumeOverflow verifies that WRITE(6) returns CHECK CONDITION with
// VOLUME OVERFLOW sense (key 0x0D) when exceeding media capacity.
func TestWrite_VolumeOverflow(t *testing.T) {
	media := tapesim.NewMedia(256)
	h := NewTapeHandler(media)

	// Write 300 bytes into 256-byte media.
	data := make([]byte, 300)
	cmd := writeCmdBuf(data, false)
	resp, err := h.HandleCommand(cmd)
	mustCheckCondition(t, resp, err)

	sense := resp.SenseBuffer()
	if len(sense) < 3 {
		t.Fatalf("sense buffer too short: %d", len(sense))
	}
	senseKey := sense[2] & 0x0F
	if senseKey != 0x0D {
		t.Fatalf("expected sense key 0x0D (VOLUME OVERFLOW), got 0x%02x", senseKey)
	}
}

// TestRead_Variable verifies variable-mode READ(6) reads the correct data back.
func TestRead_Variable(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	want := []byte("test data")
	if _, s := media.Write(want, false); s != nil {
		t.Fatalf("test setup: media.Write: %v", s)
	}
	rewindMedia(t, h)

	cmd, buf := readCmd(len(want), false, false)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	if string(buf[:len(want)]) != string(want) {
		t.Fatalf("expected %q, got %q", want, buf[:len(want)])
	}
}

// TestRead_Fixed verifies fixed-mode READ(6) reads blockCount*blockSize bytes.
func TestRead_Fixed(t *testing.T) {
	const blockSize = 512
	media := tapesim.NewMedia(1024 * 1024)
	media.SetBlockSize(blockSize)
	h := NewTapeHandler(media)

	want := make([]byte, 2*blockSize)
	for i := range want {
		want[i] = byte(i % 256)
	}
	if _, s := media.Write(want, true); s != nil {
		t.Fatalf("test setup: media.Write: %v", s)
	}
	rewindMedia(t, h)

	cmd, buf := readCmdFixed(2, blockSize, true, false)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	if string(buf) != string(want) {
		t.Fatalf("data mismatch after fixed-mode read")
	}
}

// TestRead_ILI verifies that READ(6) returns ILI sense with correct residue
// in the INFORMATION field when fewer bytes are available than requested.
// Partial data must be returned before the sense.
func TestRead_ILI(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	// Write 50 bytes, then inject a short read of 50 bytes when 100 are requested.
	payload := make([]byte, 50)
	for i := range payload {
		payload[i] = byte(i)
	}
	if _, s := media.Write(payload, false); s != nil {
		t.Fatalf("test setup: media.Write: %v", s)
	}
	rewindMedia(t, h)

	// Inject short read: actual=50 for opcode 0x08 (READ).
	media.InjectShortRead(0x08, 50)

	cmd, buf := readCmd(100, false, false)
	resp, err := h.HandleCommand(cmd)
	mustCheckCondition(t, resp, err)

	sense := resp.SenseBuffer()
	if len(sense) < 7 {
		t.Fatalf("sense buffer too short: %d", len(sense))
	}
	// ILI bit (bit 5 of byte 2) must be set.
	if sense[2]&0x20 == 0 {
		t.Fatalf("expected ILI bit (byte 2 bit 5) set, byte 2 = 0x%02x", sense[2])
	}
	// INFORMATION field (bytes 3-6) = residue = 100 - 50 = 50.
	residue := uint32(sense[3])<<24 | uint32(sense[4])<<16 | uint32(sense[5])<<8 | uint32(sense[6])
	if residue != 50 {
		t.Fatalf("expected residue 50 in INFORMATION field, got %d", residue)
	}
	// Partial data must be present in buf.
	if string(buf[:50]) != string(payload) {
		t.Fatalf("partial data mismatch before ILI sense")
	}
}

// TestRead_SILI verifies that READ(6) with SILI bit set suppresses ILI sense
// and returns GOOD with the available partial data.
func TestRead_SILI(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	payload := make([]byte, 50)
	for i := range payload {
		payload[i] = byte(i + 1)
	}
	if _, s := media.Write(payload, false); s != nil {
		t.Fatalf("test setup: media.Write: %v", s)
	}
	rewindMedia(t, h)

	// Inject short read of 50 when 100 requested, with SILI set.
	media.InjectShortRead(0x08, 50)

	cmd, buf := readCmd(100, false, true) // sili=true
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err) // SILI suppresses the ILI sense

	if string(buf[:50]) != string(payload) {
		t.Fatalf("expected 50 bytes of data with SILI suppression, data mismatch")
	}
}

// TestRead_Filemark verifies that READ(6) returns FM sense (byte 2 bit 7) when
// hitting a filemark. The data before the filemark is returned as GOOD.
func TestRead_Filemark(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	// Write some data, then a filemark.
	data := []byte("before mark")
	if _, s := media.Write(data, false); s != nil {
		t.Fatalf("test setup: media.Write: %v", s)
	}
	if s := media.WriteFilemarks(1); s != nil {
		t.Fatalf("test setup: media.WriteFilemarks: %v", s)
	}

	// First read: gets the data GOOD.
	rewindMedia(t, h)
	cmd1, buf1 := readCmd(len(data), false, false)
	resp1, err1 := h.HandleCommand(cmd1)
	mustOk(t, resp1, err1)
	if string(buf1) != string(data) {
		t.Fatalf("expected %q before filemark, got %q", data, buf1)
	}

	// Second read: hits the filemark -> CHECK CONDITION with FM bit.
	cmd2, _ := readCmd(100, false, false)
	resp2, err2 := h.HandleCommand(cmd2)
	mustCheckCondition(t, resp2, err2)

	sense := resp2.SenseBuffer()
	if len(sense) < 3 {
		t.Fatalf("sense buffer too short: %d", len(sense))
	}
	// FM bit (bit 7 of byte 2) must be set.
	if sense[2]&0x80 == 0 {
		t.Fatalf("expected FM bit (byte 2 bit 7) set, byte 2 = 0x%02x", sense[2])
	}
}

// TestRead_BlankCheck verifies that READ(6) on fresh (unwritten) media returns
// CHECK CONDITION with sense key 0x08 (BLANK CHECK).
func TestRead_BlankCheck(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	cmd, _ := readCmd(100, false, false)
	resp, err := h.HandleCommand(cmd)
	mustCheckCondition(t, resp, err)

	sense := resp.SenseBuffer()
	if len(sense) < 3 {
		t.Fatalf("sense buffer too short: %d", len(sense))
	}
	senseKey := sense[2] & 0x0F
	if senseKey != 0x08 {
		t.Fatalf("expected sense key 0x08 (BLANK CHECK), got 0x%02x", senseKey)
	}
}

// TestWriteFilemarks verifies that WRITE FILEMARKS(6) records the requested
// number of filemarks at the current position.
func TestWriteFilemarks(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	// CDB: opcode=0x10, count=3
	cdb := []byte{0x10, 0, 0, 0, 3, 0}
	cmd := tcmu.NewTestSCSICmd(cdb, nil, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	if media.FilemarkCount() != 3 {
		t.Fatalf("expected 3 filemarks, got %d", media.FilemarkCount())
	}
}

// TestSpace_Blocks verifies that SPACE(6) with code=0 (blocks) advances the
// position by count blocks in variable mode.
func TestSpace_Blocks(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	// Write 100 bytes to advance written marker.
	data := make([]byte, 100)
	if _, s := media.Write(data, false); s != nil {
		t.Fatalf("test setup: media.Write: %v", s)
	}

	// Rewind then SPACE forward 50 blocks (variable mode: 50 bytes).
	rewindMedia(t, h)

	// CDB: opcode=0x11, code=0 (blocks), count=50
	cdb := []byte{0x11, 0x00, 0, 0, 50, 0}
	cmd := tcmu.NewTestSCSICmd(cdb, nil, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	if media.Position() != 50 {
		t.Fatalf("expected position 50 after space 50 blocks, got %d", media.Position())
	}
}

// TestSpace_Filemarks verifies that SPACE(6) with code=1 (filemarks) moves
// past filemarks to position just after the target filemark.
func TestSpace_Filemarks(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	// Write 50 bytes, filemark, 50 bytes.
	data := make([]byte, 50)
	if _, s := media.Write(data, false); s != nil {
		t.Fatalf("test setup: media.Write: %v", s)
	}
	if s := media.WriteFilemarks(1); s != nil {
		t.Fatalf("test setup: media.WriteFilemarks: %v", s)
	}
	if _, s := media.Write(data, false); s != nil {
		t.Fatalf("test setup: media.Write: %v", s)
	}

	// Rewind, then SPACE 1 filemark forward.
	rewindMedia(t, h)

	cdb := []byte{0x11, 0x01, 0, 0, 1, 0}
	cmd := tcmu.NewTestSCSICmd(cdb, nil, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	// After spacing 1 filemark, position should be at the filemark (50).
	if media.Position() != 50 {
		t.Fatalf("expected position 50 after space 1 filemark, got %d", media.Position())
	}
}

// TestSpace_EndOfData verifies that SPACE(6) with code=3 (end-of-data)
// positions the tape at the written marker regardless of count.
func TestSpace_EndOfData(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	// Write 100 bytes so written=100.
	data := make([]byte, 100)
	if _, s := media.Write(data, false); s != nil {
		t.Fatalf("test setup: media.Write: %v", s)
	}

	// Rewind, then SPACE to EOD (code=3, count=0).
	rewindMedia(t, h)

	cdb := []byte{0x11, 0x03, 0, 0, 0, 0}
	cmd := tcmu.NewTestSCSICmd(cdb, nil, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	if media.Position() != 100 {
		t.Fatalf("expected position 100 at EOD, got %d", media.Position())
	}
}

// TestSpace_Backward verifies that SPACE(6) with a negative count moves
// the tape backward (clamped at beginning of tape).
func TestSpace_Backward(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	// Write 100 bytes.
	data := make([]byte, 100)
	if _, s := media.Write(data, false); s != nil {
		t.Fatalf("test setup: media.Write: %v", s)
	}

	// SPACE backward 50 blocks from position 100 (variable mode: step=1).
	// -50 in 24-bit two's complement: 0xFFFFCE
	cdb := []byte{0x11, 0x00, 0xFF, 0xFF, 0xCE, 0}
	cmd := tcmu.NewTestSCSICmd(cdb, nil, 96)
	resp, err := h.HandleCommand(cmd)
	// Backward spacing may return BOP sense or Ok depending on how far we go.
	// Here 100-50=50, which is valid (no BOP), so expect Ok.
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = resp // either Ok or a BOP sense is acceptable; verify position

	if media.Position() != 50 {
		t.Fatalf("expected position 50 after backward space, got %d", media.Position())
	}
}

// ---------------------------------------------------------------------------
// MODE SENSE(6) tests
// ---------------------------------------------------------------------------

// TestModeSense6_CompressionPage verifies MODE SENSE(6) with page 0x0F returns
// a 28-byte response: 4-byte header + 8-byte block descriptor + 16-byte
// compression page.
func TestModeSense6_CompressionPage(t *testing.T) {
	media := tapesim.NewMedia(1024*1024, tapesim.WithDensityCode(0x00))
	h := NewTapeHandler(media)

	cdb := []byte{0x1A, 0, 0x0F, 0, 255, 0}
	dataBuf := make([]byte, 256)
	cmd := tcmu.NewTestSCSICmd(cdb, dataBuf, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	// byte 0: mode data length = total - 1 = 28 - 1 = 27
	if dataBuf[0] != 27 {
		t.Fatalf("expected mode data length 27, got %d", dataBuf[0])
	}
	// byte 2: medium type must be 0x00 for virtual tape
	if dataBuf[2] != 0x00 {
		t.Fatalf("expected medium type 0x00 at byte 2, got 0x%02x", dataBuf[2])
	}
	// byte 3: block descriptor length = 8
	if dataBuf[3] != 8 {
		t.Fatalf("expected block descriptor length 8, got %d", dataBuf[3])
	}
	// byte 12: compression page code = 0x0F
	if dataBuf[12] != 0x0F {
		t.Fatalf("expected page code 0x0F at byte 12, got 0x%02x", dataBuf[12])
	}
	// byte 13: page length = 0x0E (14)
	if dataBuf[13] != 0x0E {
		t.Fatalf("expected page length 0x0E at byte 13, got 0x%02x", dataBuf[13])
	}
}

// TestModeSense6_AllPages verifies MODE SENSE(6) with page 0x3F (all pages)
// returns the same 28-byte response as page 0x0F.
func TestModeSense6_AllPages(t *testing.T) {
	media := tapesim.NewMedia(1024*1024, tapesim.WithDensityCode(0x00))
	h := NewTapeHandler(media)

	cdb := []byte{0x1A, 0, 0x3F, 0, 255, 0}
	dataBuf := make([]byte, 256)
	cmd := tcmu.NewTestSCSICmd(cdb, dataBuf, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	if dataBuf[0] != 27 {
		t.Fatalf("expected mode data length 27 for 0x3F, got %d", dataBuf[0])
	}
	if dataBuf[12] != 0x0F {
		t.Fatalf("expected compression page code 0x0F at byte 12, got 0x%02x", dataBuf[12])
	}
}

// TestModeSense6_BlockDescriptor verifies that MODE SENSE(6) includes the
// correct density code and block size in the block descriptor.
func TestModeSense6_BlockDescriptor(t *testing.T) {
	media := tapesim.NewMedia(1024*1024, tapesim.WithBlockSize(1024), tapesim.WithDensityCode(0x46))
	h := NewTapeHandler(media)

	cdb := []byte{0x1A, 0, 0x00, 0, 255, 0}
	dataBuf := make([]byte, 256)
	cmd := tcmu.NewTestSCSICmd(cdb, dataBuf, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	// byte 4: density code = 0x46
	if dataBuf[4] != 0x46 {
		t.Fatalf("expected density code 0x46 at byte 4, got 0x%02x", dataBuf[4])
	}
	// bytes 9-11: block length = 1024 = 0x000400
	if dataBuf[9] != 0x00 || dataBuf[10] != 0x04 || dataBuf[11] != 0x00 {
		t.Fatalf("expected block size 0x000400 at bytes 9-11, got 0x%02x%02x%02x",
			dataBuf[9], dataBuf[10], dataBuf[11])
	}
}

// TestModeSense6_NoPage verifies MODE SENSE(6) with page 0x00 returns a
// 12-byte response (header + block descriptor only, no pages).
func TestModeSense6_NoPage(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	cdb := []byte{0x1A, 0, 0x00, 0, 255, 0}
	dataBuf := make([]byte, 256)
	cmd := tcmu.NewTestSCSICmd(cdb, dataBuf, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	// byte 0: mode data length = 12 - 1 = 11
	if dataBuf[0] != 11 {
		t.Fatalf("expected mode data length 11 for page 0x00, got %d", dataBuf[0])
	}
	// No page data: byte 12 should be zero (not set by handler).
	if dataBuf[12] != 0x00 {
		t.Fatalf("expected no page data (byte 12 = 0), got 0x%02x", dataBuf[12])
	}
}

// TestModeSense6_Compression_DCE_DDE verifies that DCE and DDE bits are
// correctly reported in the compression page when both are set.
func TestModeSense6_Compression_DCE_DDE(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	media.SetCompression(true, true)
	h := NewTapeHandler(media)

	cdb := []byte{0x1A, 0, 0x0F, 0, 255, 0}
	dataBuf := make([]byte, 256)
	cmd := tcmu.NewTestSCSICmd(cdb, dataBuf, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	// byte 14: compression page data byte 2 (offset 12+2=14), DCE = bit 7
	if dataBuf[14]&0x80 == 0 {
		t.Fatalf("expected DCE bit (byte 14 bit 7) set, byte 14 = 0x%02x", dataBuf[14])
	}
	// byte 15: compression page data byte 3 (offset 12+3=15), DDE = bit 7
	if dataBuf[15]&0x80 == 0 {
		t.Fatalf("expected DDE bit (byte 15 bit 7) set, byte 15 = 0x%02x", dataBuf[15])
	}
}

// ---------------------------------------------------------------------------
// MODE SELECT(6) tests
// ---------------------------------------------------------------------------

// TestModeSelect6_BlockSize verifies that MODE SELECT(6) with a block descriptor
// updates the media block size to the value in the descriptor.
func TestModeSelect6_BlockSize(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	// Build parameter: 4-byte header (bdLen=8) + 8-byte block descriptor.
	// Block size 1024 = 0x000400 at bytes 5-7 of the block descriptor.
	paramData := make([]byte, 12)
	paramData[3] = 8    // block descriptor length
	paramData[9] = 0x00 // block size byte 0 (most significant of 24-bit)
	paramData[10] = 0x04 // block size byte 1
	paramData[11] = 0x00 // block size byte 2 (least significant)

	cdb := []byte{0x15, 0x10, 0, 0, 12, 0} // PF=1, paramLen=12
	cmd := tcmu.NewTestSCSICmd(cdb, paramData, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	if media.BlockSize() != 1024 {
		t.Fatalf("expected block size 1024 after MODE SELECT, got %d", media.BlockSize())
	}
}

// TestModeSelect6_Compression verifies that MODE SELECT(6) with a compression
// page updates the media DCE and DDE flags.
func TestModeSelect6_Compression(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	// Parameter: 4-byte header (no BD) + compression page.
	// Page 0x0F, length 0x0E (14 bytes), DCE=1 (byte 2 bit 7), DDE=1 (byte 3 bit 7).
	paramData := []byte{
		0x00, 0x00, 0x00, 0x00, // header: bdLen=0
		0x0F, 0x0E,             // page code and length
		0x80, 0x80,             // DCE bit 7, DDE bit 7
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, // 12 reserved bytes = 14 total page data
	}

	cdb := []byte{0x15, 0x10, 0, 0, byte(len(paramData)), 0}
	cmd := tcmu.NewTestSCSICmd(cdb, paramData, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	dce, dde := media.Compression()
	if !dce {
		t.Fatalf("expected DCE=true after MODE SELECT compression page")
	}
	if !dde {
		t.Fatalf("expected DDE=true after MODE SELECT compression page")
	}
}

// TestModeSelect6_Both verifies MODE SELECT(6) with both block descriptor and
// compression page in a single command.
func TestModeSelect6_Both(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	// 4-byte header + 8-byte block descriptor (bs=512) + compression page (16 bytes).
	// Total = 4 + 8 + 2 (page header) + 14 (page data) = 28 bytes.
	paramData := make([]byte, 28)
	paramData[3] = 8    // bdLen
	paramData[9] = 0x00 // block size 512 = 0x000200
	paramData[10] = 0x02
	paramData[11] = 0x00
	// compression page starts at offset 12
	paramData[12] = 0x0F // page code
	paramData[13] = 0x0E // page length = 14
	paramData[14] = 0x80 // DCE
	paramData[15] = 0x00 // DDE = false
	// bytes 16-27: remaining 12 bytes of page data (total page data = 14)

	cdb := []byte{0x15, 0x10, 0, 0, byte(len(paramData)), 0} // paramLen=28
	cmd := tcmu.NewTestSCSICmd(cdb, paramData, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	if media.BlockSize() != 512 {
		t.Fatalf("expected block size 512, got %d", media.BlockSize())
	}
	dce, dde := media.Compression()
	if !dce {
		t.Fatalf("expected DCE=true")
	}
	if dde {
		t.Fatalf("expected DDE=false")
	}
}

// ---------------------------------------------------------------------------
// REPORT DENSITY SUPPORT tests
// ---------------------------------------------------------------------------

// TestReportDensitySupport verifies that REPORT DENSITY SUPPORT returns a
// 56-byte response (4-byte header + 52-byte descriptor) with correct header
// available data length and "UISCSI" organization field.
func TestReportDensitySupport(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	// CDB: allocLen=256 in bytes 7-8
	cdb := []byte{0x44, 0, 0, 0, 0, 0, 0, 0x01, 0x00, 0}
	dataBuf := make([]byte, 256)
	cmd := tcmu.NewTestSCSICmd(cdb, dataBuf, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	// bytes 0-1: available data length = 52 (one descriptor)
	availLen := int(dataBuf[0])<<8 | int(dataBuf[1])
	if availLen != 52 {
		t.Fatalf("expected available data length 52, got %d", availLen)
	}
	// bytes 20-27 (header 4 + descriptor offset 16-23) = organization "UISCSI  "
	org := string(dataBuf[20:28])
	if org != "UISCSI  " {
		t.Fatalf("expected organization 'UISCSI  ', got %q", org)
	}
}

// TestReportDensitySupport_CustomCode verifies that WithDensityCode sets the
// primary density code in the descriptor.
func TestReportDensitySupport_CustomCode(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media, WithDensityCode(0x46))

	cdb := []byte{0x44, 0, 0, 0, 0, 0, 0, 0x01, 0x00, 0}
	dataBuf := make([]byte, 256)
	cmd := tcmu.NewTestSCSICmd(cdb, dataBuf, 96)
	resp, err := h.HandleCommand(cmd)
	mustOk(t, resp, err)

	// byte 4: primary density code (first byte of descriptor, at offset 4)
	if dataBuf[4] != 0x46 {
		t.Fatalf("expected density code 0x46 at byte 4, got 0x%02x", dataBuf[4])
	}
}

// ---------------------------------------------------------------------------
// Full integration test
// ---------------------------------------------------------------------------

// TestFullSuite_AllCommands exercises a realistic command sequence through all
// 13 supported commands, verifying the complete handler without errors.
func TestFullSuite_AllCommands(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	h := NewTapeHandler(media)

	// 1. TEST UNIT READY
	{
		cdb := []byte{0x00, 0, 0, 0, 0, 0}
		resp, err := h.HandleCommand(tcmu.NewTestSCSICmd(cdb, nil, 96))
		mustOk(t, resp, err)
	}

	// 2. INQUIRY
	{
		cdb := []byte{0x12, 0, 0, 0, 36, 0}
		dataBuf := make([]byte, 36)
		resp, err := h.HandleCommand(tcmu.NewTestSCSICmd(cdb, dataBuf, 96))
		mustOk(t, resp, err)
		if dataBuf[0] != 0x01 {
			t.Fatalf("INQUIRY: expected device type 0x01, got 0x%02x", dataBuf[0])
		}
	}

	// 3. MODE SELECT: set block size 0 (variable)
	{
		paramData := make([]byte, 12)
		paramData[3] = 8 // bdLen
		// block size = 0 (variable) — bytes 9-11 remain 0
		cdb := []byte{0x15, 0x10, 0, 0, 12, 0}
		resp, err := h.HandleCommand(tcmu.NewTestSCSICmd(cdb, paramData, 96))
		mustOk(t, resp, err)
	}

	// 4. WRITE 100 bytes variable
	const payload = "hello tape world, this is a 100-byte test payload padded to fill the block"
	writeData := make([]byte, 100)
	copy(writeData, payload)
	{
		cdb := []byte{0x0A, 0, 0, 0, 100, 0}
		buf := make([]byte, 100)
		copy(buf, writeData)
		resp, err := h.HandleCommand(tcmu.NewTestSCSICmd(cdb, buf, 96))
		mustOk(t, resp, err)
	}

	// 5. WRITE FILEMARKS 1
	{
		cdb := []byte{0x10, 0, 0, 0, 1, 0}
		resp, err := h.HandleCommand(tcmu.NewTestSCSICmd(cdb, nil, 96))
		mustOk(t, resp, err)
	}

	// 6. WRITE another 100 bytes
	{
		cdb := []byte{0x0A, 0, 0, 0, 100, 0}
		buf := make([]byte, 100)
		resp, err := h.HandleCommand(tcmu.NewTestSCSICmd(cdb, buf, 96))
		mustOk(t, resp, err)
	}

	// 7. REWIND
	{
		cdb := []byte{0x01, 0, 0, 0, 0, 0}
		resp, err := h.HandleCommand(tcmu.NewTestSCSICmd(cdb, nil, 96))
		mustOk(t, resp, err)
	}

	// 8. READ 100 bytes -> Ok, data matches
	{
		cdb := []byte{0x08, 0, 0, 0, 100, 0}
		dataBuf := make([]byte, 100)
		resp, err := h.HandleCommand(tcmu.NewTestSCSICmd(cdb, dataBuf, 96))
		mustOk(t, resp, err)
		if string(dataBuf[:len(payload)]) != payload {
			t.Fatalf("read data mismatch: got %q", dataBuf[:len(payload)])
		}
	}

	// 9. READ -> should hit filemark (CHECK CONDITION with FM bit)
	{
		cdb := []byte{0x08, 0, 0, 0, 100, 0}
		dataBuf := make([]byte, 100)
		resp, err := h.HandleCommand(tcmu.NewTestSCSICmd(cdb, dataBuf, 96))
		mustCheckCondition(t, resp, err)
		sense := resp.SenseBuffer()
		if len(sense) < 3 || sense[2]&0x80 == 0 {
			t.Fatalf("expected FM bit set in sense after filemark, byte 2 = 0x%02x", sense[2])
		}
	}

	// 10. SPACE to EOD (code=3, count=0)
	{
		cdb := []byte{0x11, 0x03, 0, 0, 0, 0}
		resp, err := h.HandleCommand(tcmu.NewTestSCSICmd(cdb, nil, 96))
		mustOk(t, resp, err)
	}

	// 11. READ POSITION -> verify position
	{
		cdb := []byte{0x34, 0, 0, 0, 0, 0, 0, 0, 0, 0}
		dataBuf := make([]byte, 20)
		resp, err := h.HandleCommand(tcmu.NewTestSCSICmd(cdb, dataBuf, 96))
		mustOk(t, resp, err)
		pos := uint32(dataBuf[4])<<24 | uint32(dataBuf[5])<<16 | uint32(dataBuf[6])<<8 | uint32(dataBuf[7])
		if pos == 0 {
			t.Fatal("expected non-zero position after writes and spacing to EOD")
		}
	}

	// 12. READ BLOCK LIMITS
	{
		cdb := []byte{0x05, 0, 0, 0, 0, 0}
		dataBuf := make([]byte, 6)
		resp, err := h.HandleCommand(tcmu.NewTestSCSICmd(cdb, dataBuf, 96))
		mustOk(t, resp, err)
	}

	// 13. MODE SENSE page 0x0F -> compression page present
	{
		cdb := []byte{0x1A, 0, 0x0F, 0, 255, 0}
		dataBuf := make([]byte, 256)
		resp, err := h.HandleCommand(tcmu.NewTestSCSICmd(cdb, dataBuf, 96))
		mustOk(t, resp, err)
		if dataBuf[12] != 0x0F {
			t.Fatalf("MODE SENSE: expected compression page code 0x0F, got 0x%02x", dataBuf[12])
		}
	}

	// 14. REPORT DENSITY SUPPORT -> descriptor present
	{
		cdb := []byte{0x44, 0, 0, 0, 0, 0, 0, 0x01, 0x00, 0}
		dataBuf := make([]byte, 256)
		resp, err := h.HandleCommand(tcmu.NewTestSCSICmd(cdb, dataBuf, 96))
		mustOk(t, resp, err)
		availLen := int(dataBuf[0])<<8 | int(dataBuf[1])
		if availLen != 52 {
			t.Fatalf("REPORT DENSITY SUPPORT: expected available length 52, got %d", availLen)
		}
	}
}

// TestNewTapeDevReady verifies that NewTapeDevReady returns a valid DevReadyFunc
// that processes commands sequentially through the TapeHandler via channel dispatch.
func TestNewTapeDevReady(t *testing.T) {
	media := tapesim.NewMedia(1024 * 1024)
	devReady := NewTapeDevReady(media)

	cmdChan := make(chan *tcmu.SCSICmd, 1)
	respChan := make(chan tcmu.SCSIResponse, 1)

	// Start the DevReady goroutine.
	if err := devReady(cmdChan, respChan); err != nil {
		t.Fatalf("DevReady returned error: %v", err)
	}

	// Send a TUR command through the channel.
	cdb := []byte{0x00, 0, 0, 0, 0, 0}
	cmd := tcmu.NewTestSCSICmd(cdb, nil, 96)
	cmdChan <- cmd
	resp := <-respChan

	if resp.Status() != 0x00 {
		t.Fatalf("expected GOOD status from DevReady TUR, got 0x%02x", resp.Status())
	}

	// Signal handler goroutine to exit.
	close(cmdChan)
	// Drain respChan so the goroutine (which defers close(out)) can exit cleanly.
	for range respChan {
	}
}
