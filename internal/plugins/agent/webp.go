package agent

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"

	nativewebp "github.com/HugoSmits86/nativewebp"
	_ "golang.org/x/image/webp"
)

// 隐藏图参数与 https://apng.rem.asia/ 的 WebP 模式保持一致：
// 第一帧为表层图，仅显示 10 毫秒；第二帧为隐藏图，停留 999 秒。
const (
	surfaceFrameDurationMs = 10
	hiddenFrameDurationMs  = 999000
)

// VP8X 标志位：bit1 为动画位。帧位流为 VP8L/VP8 且不含 ALPH 块，因此不置 alpha 位（bit4）。
const webpFlagAnimation = 0x02

// BuildHiddenWebP 生成双帧隐藏图 WebP：第一帧短暂显示表层图，第二帧长时间停留隐藏图。
func BuildHiddenWebP(surfaceImagePath, hiddenImagePath, outputPath string) (string, error) {
	surfaceImage, err := openImage(surfaceImagePath)
	if err != nil {
		return "", fmt.Errorf("读取表层图片: %w", err)
	}

	hiddenImage, err := openImage(hiddenImagePath)
	if err != nil {
		return "", fmt.Errorf("读取隐藏图片: %w", err)
	}

	// 输出尺寸由隐藏图片决定，表层图等比缩放后居中铺在白色画布上。
	width, height := imageDimensions(hiddenImage.Bounds())

	surfaceBitstream, err := encodeWebPFrameBitstream(drawOnCanvas(surfaceImage, width, height))
	if err != nil {
		return "", fmt.Errorf("编码表层图片帧: %w", err)
	}

	hiddenBitstream, err := encodeWebPFrameBitstream(drawOnCanvas(hiddenImage, width, height))
	if err != nil {
		return "", fmt.Errorf("编码隐藏图片帧: %w", err)
	}

	data, err := buildAnimatedWebP(surfaceBitstream, hiddenBitstream, width, height)
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(outputPath, data, 0644); err != nil {
		return "", fmt.Errorf("写入 WebP 文件: %w", err)
	}

	return outputPath, nil
}

func openImage(path string) (image.Image, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	img, _, err := image.Decode(file)
	if err != nil {
		return nil, err
	}

	return img, nil
}

func imageDimensions(bounds image.Rectangle) (int, int) {
	return bounds.Dx(), bounds.Dy()
}

func drawOnCanvas(src image.Image, width, height int) image.Image {
	canvasRect := image.Rect(0, 0, width, height)
	canvas := image.NewNRGBA(canvasRect)
	draw.Draw(canvas, canvasRect, image.NewUniform(color.White), image.Point{}, draw.Src)

	srcBounds := src.Bounds()
	dstRect := fitRect(srcBounds.Dx(), srcBounds.Dy(), width, height)
	if dstRect.Dx() == srcBounds.Dx() && dstRect.Dy() == srcBounds.Dy() {
		draw.Draw(canvas, dstRect, src, srcBounds.Min, draw.Over)
		return canvas
	}

	drawScaled(canvas, dstRect, src)
	return canvas
}

func fitRect(srcWidth, srcHeight, canvasWidth, canvasHeight int) image.Rectangle {
	if srcWidth <= 0 || srcHeight <= 0 || canvasWidth <= 0 || canvasHeight <= 0 {
		return image.Rect(0, 0, canvasWidth, canvasHeight)
	}

	widthRatio := float64(canvasWidth) / float64(srcWidth)
	heightRatio := float64(canvasHeight) / float64(srcHeight)
	scale := widthRatio
	if heightRatio < scale {
		scale = heightRatio
	}

	dstWidth := max(1, int(float64(srcWidth)*scale+0.5))
	dstHeight := max(1, int(float64(srcHeight)*scale+0.5))
	left := (canvasWidth - dstWidth) / 2
	top := (canvasHeight - dstHeight) / 2
	return image.Rect(left, top, left+dstWidth, top+dstHeight)
}

func drawScaled(dst draw.Image, dstRect image.Rectangle, src image.Image) {
	srcBounds := src.Bounds()
	if dstRect.Empty() || srcBounds.Empty() {
		return
	}

	scaleX := float64(srcBounds.Dx()) / float64(dstRect.Dx())
	scaleY := float64(srcBounds.Dy()) / float64(dstRect.Dy())
	for y := dstRect.Min.Y; y < dstRect.Max.Y; y++ {
		srcY := srcBounds.Min.Y + min(srcBounds.Dy()-1, int(float64(y-dstRect.Min.Y)*scaleY))
		for x := dstRect.Min.X; x < dstRect.Max.X; x++ {
			srcX := srcBounds.Min.X + min(srcBounds.Dx()-1, int(float64(x-dstRect.Min.X)*scaleX))
			dst.Set(x, y, src.At(srcX, srcY))
		}
	}
}

// encodeWebPFrameBitstream 把单帧编码为无损 VP8L WebP，并抽出可直接嵌入 ANMF 的「FourCC + 位流」子块。
func encodeWebPFrameBitstream(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := nativewebp.Encode(&buf, img, nil); err != nil {
		return nil, err
	}

	return extractWebPFrameBitstream(buf.Bytes())
}

// extractWebPFrameBitstream 从完整的 WebP 文件中取出 VP8L / VP8 子块（含 8 字节子块头）。
func extractWebPFrameBitstream(data []byte) ([]byte, error) {
	if len(data) < 20 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return nil, fmt.Errorf("WebP 数据无效")
	}

	for offset := 12; offset+8 <= len(data); {
		tag := string(data[offset : offset+4])
		size := int(binary.LittleEndian.Uint32(data[offset+4 : offset+8]))
		start := offset + 8
		if size < 0 || start+size > len(data) {
			return nil, fmt.Errorf("WebP 数据不完整")
		}

		// 有损 VP8 必定包含 10 字节帧头，用它校验数据完整性。
		if tag == "VP8L" || (tag == "VP8 " && size >= 10) {
			return data[offset : start+size], nil
		}

		offset = start + size + size%2
	}

	return nil, fmt.Errorf("WebP 缺少图像位流")
}

// buildAnimatedWebP 组装 RIFF 容器：WEBP + VP8X + ANIM + 两个 ANMF 帧。
func buildAnimatedWebP(surfaceBitstream, hiddenBitstream []byte, width, height int) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("图片尺寸无效")
	}
	if width > 1<<24 || height > 1<<24 {
		return nil, fmt.Errorf("图片尺寸超出 WebP 限制")
	}

	vp8x := make([]byte, 10)
	vp8x[0] = webpFlagAnimation
	putUint24LE(vp8x[4:7], width-1)
	putUint24LE(vp8x[7:10], height-1)

	anim := make([]byte, 6)
	copy(anim[0:4], []byte{0xff, 0xff, 0xff, 0xff}) // 背景色，帧不透明因此不会用到
	// 循环次数 0 表示无限循环，保证隐藏帧能稳定停留在画面里。
	binary.LittleEndian.PutUint16(anim[4:6], 0)

	var payload bytes.Buffer
	payload.WriteString("WEBP")
	payload.Write(makeRIFFChunk("VP8X", vp8x))
	payload.Write(makeRIFFChunk("ANIM", anim))
	payload.Write(makeANMFChunk(surfaceBitstream, width, height, surfaceFrameDurationMs))
	payload.Write(makeANMFChunk(hiddenBitstream, width, height, hiddenFrameDurationMs))

	// RIFF size 从 "WEBP"（Form Type）起算，即「文件总长 - 8」。
	out := make([]byte, 8+payload.Len())
	copy(out[0:4], "RIFF")
	binary.LittleEndian.PutUint32(out[4:8], uint32(payload.Len()))
	copy(out[8:], payload.Bytes())

	return out, nil
}

// makeANMFChunk 组装单个动画帧：16 字节帧头 + 图像位流子块。
func makeANMFChunk(bitstream []byte, width, height, durationMs int) []byte {
	header := make([]byte, 16)
	putUint24LE(header[0:3], 0) // 帧左上角 X
	putUint24LE(header[3:6], 0) // 帧左上角 Y
	putUint24LE(header[6:9], width-1)
	putUint24LE(header[9:12], height-1)
	putUint24LE(header[12:15], durationMs)
	header[15] = 0 // 处置方式：不混合、不处置

	body := bitstream
	if len(body)%2 == 1 {
		// 帧内子块同样需要偶数对齐，否则后续块解析会错位。
		body = append(append([]byte{}, body...), 0)
	}

	return makeRIFFChunk("ANMF", append(header, body...))
}

func makeRIFFChunk(chunkType string, payload []byte) []byte {
	padding := len(payload) % 2
	chunk := make([]byte, 8+len(payload)+padding)
	copy(chunk[0:4], chunkType)
	binary.LittleEndian.PutUint32(chunk[4:8], uint32(len(payload)))
	copy(chunk[8:], payload)

	return chunk
}

func putUint24LE(dst []byte, value int) {
	dst[0] = byte(value)
	dst[1] = byte(value >> 8)
	dst[2] = byte(value >> 16)
}
