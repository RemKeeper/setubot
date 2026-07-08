package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"
)

var pngSignature = []byte{137, 80, 78, 71, 13, 10, 26, 10}

const (
	firstFrameDelayNumerator    = 1
	firstFrameDelayDenominator  = 100
	secondFrameDelayNumerator   = 999
	secondFrameDelayDenominator = 1
)

type pngChunk struct {
	chunkType string
	data      []byte
}

func BuildHiddenAPNG(surfaceImagePath, hiddenImagePath, outputPath string) (string, error) {
	surfaceImage, err := openImage(surfaceImagePath)
	if err != nil {
		return "", fmt.Errorf("读取表层图片: %w", err)
	}

	hiddenImage, err := openImage(hiddenImagePath)
	if err != nil {
		return "", fmt.Errorf("读取隐藏图片: %w", err)
	}

	width, height := imageDimensions(hiddenImage.Bounds())
	surfacePNG, err := encodePNG(drawOnCanvas(surfaceImage, width, height))
	if err != nil {
		return "", fmt.Errorf("编码表层图片帧 PNG: %w", err)
	}

	hiddenPNG, err := encodePNG(drawOnCanvas(hiddenImage, width, height))
	if err != nil {
		return "", fmt.Errorf("编码隐藏图片帧 PNG: %w", err)
	}

	surfaceChunks, err := parsePNGChunks(surfacePNG)
	if err != nil {
		return "", fmt.Errorf("解析表层图片帧 PNG: %w", err)
	}

	hiddenChunks, err := parsePNGChunks(hiddenPNG)
	if err != nil {
		return "", fmt.Errorf("解析隐藏图片帧 PNG: %w", err)
	}

	outputFile, err := os.Create(outputPath)
	if err != nil {
		return "", fmt.Errorf("创建输出文件: %w", err)
	}
	defer outputFile.Close()

	if err := writeAPNG(outputFile, surfaceChunks, hiddenChunks, width, height); err != nil {
		return "", fmt.Errorf("写入 APNG: %w", err)
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

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	if err := encoder.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func parsePNGChunks(data []byte) ([]pngChunk, error) {
	if len(data) < len(pngSignature) || !bytes.Equal(data[:len(pngSignature)], pngSignature) {
		return nil, fmt.Errorf("不是有效 PNG 数据")
	}

	reader := bytes.NewReader(data[len(pngSignature):])
	chunks := make([]pngChunk, 0)

	for reader.Len() > 0 {
		var length uint32
		if err := binary.Read(reader, binary.BigEndian, &length); err != nil {
			return nil, err
		}

		typeBytes := make([]byte, 4)
		if _, err := io.ReadFull(reader, typeBytes); err != nil {
			return nil, err
		}

		chunkData := make([]byte, length)
		if _, err := io.ReadFull(reader, chunkData); err != nil {
			return nil, err
		}

		var crc uint32
		if err := binary.Read(reader, binary.BigEndian, &crc); err != nil {
			return nil, err
		}

		chunks = append(chunks, pngChunk{chunkType: string(typeBytes), data: chunkData})
		if string(typeBytes) == "IEND" {
			break
		}
	}

	return chunks, nil
}

func writeAPNG(w io.Writer, surfaceChunks, hiddenChunks []pngChunk, width, height int) error {
	if _, err := w.Write(pngSignature); err != nil {
		return err
	}

	sequenceNumber := uint32(0)
	for _, chunk := range surfaceChunks {
		switch chunk.chunkType {
		case "IHDR":
			if err := writeChunk(w, chunk.chunkType, chunk.data); err != nil {
				return err
			}
			if err := writeChunk(w, "acTL", makeACTLData(2, 0)); err != nil {
				return err
			}
			if err := writeChunk(w, "fcTL", makeFCTLData(sequenceNumber, width, height, firstFrameDelayNumerator, firstFrameDelayDenominator)); err != nil {
				return err
			}
			sequenceNumber++
		case "IDAT":
			if err := writeChunk(w, chunk.chunkType, chunk.data); err != nil {
				return err
			}
		case "IEND":
			if err := writeHiddenFrame(w, hiddenChunks, sequenceNumber, width, height); err != nil {
				return err
			}
			if err := writeChunk(w, chunk.chunkType, chunk.data); err != nil {
				return err
			}
		default:
			if err := writeChunk(w, chunk.chunkType, chunk.data); err != nil {
				return err
			}
		}
	}

	return nil
}

func writeHiddenFrame(w io.Writer, chunks []pngChunk, sequenceNumber uint32, width, height int) error {
	frameControlWritten := false

	for _, chunk := range chunks {
		if chunk.chunkType != "IDAT" {
			continue
		}

		if !frameControlWritten {
			if err := writeChunk(w, "fcTL", makeFCTLData(sequenceNumber, width, height, secondFrameDelayNumerator, secondFrameDelayDenominator)); err != nil {
				return err
			}
			sequenceNumber++
			frameControlWritten = true
		}

		if err := writeChunk(w, "fdAT", makeFDATData(sequenceNumber, chunk.data)); err != nil {
			return err
		}
		sequenceNumber++
	}

	if !frameControlWritten {
		return fmt.Errorf("隐藏图片帧没有 IDAT 数据")
	}

	return nil
}

func makeACTLData(frameCount, playCount uint32) []byte {
	data := make([]byte, 8)
	binary.BigEndian.PutUint32(data[0:4], frameCount)
	binary.BigEndian.PutUint32(data[4:8], playCount)
	return data
}

func makeFCTLData(sequenceNumber uint32, width, height, delayNumerator, delayDenominator int) []byte {
	data := make([]byte, 26)
	binary.BigEndian.PutUint32(data[0:4], sequenceNumber)
	binary.BigEndian.PutUint32(data[4:8], uint32(width))
	binary.BigEndian.PutUint32(data[8:12], uint32(height))
	binary.BigEndian.PutUint32(data[12:16], 0)
	binary.BigEndian.PutUint32(data[16:20], 0)
	binary.BigEndian.PutUint16(data[20:22], uint16(delayNumerator))
	binary.BigEndian.PutUint16(data[22:24], uint16(delayDenominator))
	data[24] = 0
	data[25] = 0
	return data
}

func makeFDATData(sequenceNumber uint32, idatData []byte) []byte {
	data := make([]byte, 4+len(idatData))
	binary.BigEndian.PutUint32(data[0:4], sequenceNumber)
	copy(data[4:], idatData)
	return data
}

func writeChunk(w io.Writer, chunkType string, data []byte) error {
	if err := binary.Write(w, binary.BigEndian, uint32(len(data))); err != nil {
		return err
	}

	typeBytes := []byte(chunkType)
	if _, err := w.Write(typeBytes); err != nil {
		return err
	}

	if _, err := w.Write(data); err != nil {
		return err
	}

	checksum := crc32.NewIEEE()
	checksum.Write(typeBytes)
	checksum.Write(data)

	return binary.Write(w, binary.BigEndian, checksum.Sum32())
}
