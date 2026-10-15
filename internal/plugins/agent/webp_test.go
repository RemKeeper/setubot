package agent

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildHiddenWebP(t *testing.T) {
	dir := t.TempDir()
	surfacePath := filepath.Join(dir, "surface.png")
	hiddenPath := filepath.Join(dir, "hidden.png")
	writeTestImage(t, surfacePath, 40, 30, color.RGBA{R: 255, A: 255})
	writeTestImage(t, hiddenPath, 100, 80, color.RGBA{B: 255, A: 255})

	outputPath := filepath.Join(dir, "out.webp")
	if _, err := BuildHiddenWebP(surfacePath, hiddenPath, outputPath); err != nil {
		t.Fatalf("BuildHiddenWebP: %v", err)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("读取输出文件: %v", err)
	}
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		t.Fatalf("输出不是有效的 WebP 文件")
	}
	if got, want := int(binary.LittleEndian.Uint32(data[4:8])), len(data)-8; got != want {
		t.Errorf("RIFF size = %d, 期望 %d", got, want)
	}

	chunks := readTestChunks(t, data[12:])
	if len(chunks) != 4 {
		t.Fatalf("子块数量 = %d, 期望 4", len(chunks))
	}
	for i, want := range []string{"VP8X", "ANIM", "ANMF", "ANMF"} {
		if chunks[i].typ != want {
			t.Fatalf("第 %d 个子块 = %s, 期望 %s", i, chunks[i].typ, want)
		}
	}

	vp8x := chunks[0].data
	if len(vp8x) != 10 || vp8x[0]&0x02 == 0 {
		t.Errorf("VP8X 标志位错误: %v", vp8x)
	}
	if got := readUint24LETest(vp8x[4:7]); got != 99 {
		t.Errorf("VP8X 宽度-1 = %d, 期望 99", got)
	}
	if got := readUint24LETest(vp8x[7:10]); got != 79 {
		t.Errorf("VP8X 高度-1 = %d, 期望 79", got)
	}
	if len(chunks[1].data) != 6 || binary.LittleEndian.Uint16(chunks[1].data[4:6]) != 0 {
		t.Errorf("ANIM 循环次数应为无限循环: %v", chunks[1].data)
	}

	surfaceFrame := checkTestFrame(t, chunks[2].data, surfaceFrameDurationMs)
	hiddenFrame := checkTestFrame(t, chunks[3].data, hiddenFrameDurationMs)

	if pixel := testFramePixel(t, surfaceFrame, 50, 40); pixel != (color.RGBA{R: 255, A: 255}) {
		t.Errorf("表层帧中心像素 = %v, 期望红色", pixel)
	}
	if pixel := testFramePixel(t, hiddenFrame, 50, 40); pixel != (color.RGBA{B: 255, A: 255}) {
		t.Errorf("隐藏帧中心像素 = %v, 期望蓝色", pixel)
	}
}

// checkTestFrame 校验 ANMF 帧头，并返回帧内图像子块（含 FourCC 与位流）。
func checkTestFrame(t *testing.T, frame []byte, wantDurationMs int) []byte {
	t.Helper()
	if len(frame) < 16 {
		t.Fatalf("ANMF 帧数据过短: %d", len(frame))
	}
	if got := readUint24LETest(frame[6:9]); got != 99 {
		t.Errorf("ANMF 宽度-1 = %d, 期望 99", got)
	}
	if got := readUint24LETest(frame[9:12]); got != 79 {
		t.Errorf("ANMF 高度-1 = %d, 期望 79", got)
	}
	if got := readUint24LETest(frame[12:15]); got != wantDurationMs {
		t.Errorf("ANMF 帧时长 = %d, 期望 %d", got, wantDurationMs)
	}

	inner := readTestChunks(t, frame[16:])
	if len(inner) != 1 || (inner[0].typ != "VP8L" && inner[0].typ != "VP8 ") {
		t.Fatalf("ANMF 内图像子块异常: %+v", inner)
	}

	// 还原「FourCC + 长度 + 位流」的完整子块，与容器中的 ANMF 内容一致。
	bitstream := make([]byte, 8+len(inner[0].data))
	copy(bitstream[0:4], inner[0].typ)
	binary.LittleEndian.PutUint32(bitstream[4:8], uint32(len(inner[0].data)))
	copy(bitstream[8:], inner[0].data)

	return bitstream
}

func testFramePixel(t *testing.T, bitstream []byte, x, y int) color.RGBA {
	t.Helper()
	// 把帧位流包回单帧 WebP 文件后用标准解码器验证内容。
	file := []byte("RIFF\x00\x00\x00\x00WEBP")
	file = append(file, bitstream...)
	binary.LittleEndian.PutUint32(file[4:8], uint32(len(file)-8))

	img, _, err := image.Decode(bytes.NewReader(file))
	if err != nil {
		t.Fatalf("解码帧位流: %v", err)
	}
	if img.Bounds().Dx() != 100 || img.Bounds().Dy() != 80 {
		t.Fatalf("帧尺寸 = %v, 期望 100x80", img.Bounds())
	}

	r, g, b, a := img.At(x, y).RGBA()
	return color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: uint8(a >> 8)}
}

func readTestChunks(t *testing.T, data []byte) []testChunk {
	t.Helper()
	chunks := make([]testChunk, 0, 4)
	for offset := 0; offset+8 <= len(data); {
		chunkType := string(data[offset : offset+4])
		size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		start := offset + 8
		if size < 0 || start+size > len(data) {
			t.Fatalf("子块 %s 长度越界", chunkType)
		}
		chunks = append(chunks, testChunk{typ: chunkType, data: data[start : start+size]})
		offset = start + size + size%2
	}

	return chunks
}

func readUint24LETest(data []byte) int {
	return int(data[0]) | int(data[1])<<8 | int(data[2])<<16
}

type testChunk struct {
	typ  string
	data []byte
}

func writeTestImage(t *testing.T, path string, width, height int, fill color.RGBA) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: fill.R, G: fill.G, B: fill.B, A: fill.A})
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("编码测试图片: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0644); err != nil {
		t.Fatalf("写入测试图片: %v", err)
	}
}
