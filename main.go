package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nicksanford/imageclock/clockdrawer"
	"golang.org/x/exp/maps"

	"go.viam.com/rdk/logging"
)

func init() {
	slices.Sort(colorOptions)
}

func main() {
	ContextualMain(realMain, logging.NewLogger("imageclock"))
}

func run(ctx context.Context, a []string, logger logging.Logger) error {
	return nil
}

func realMain(ctx context.Context, a []string, logger logging.Logger) error {
	if len(a) >= 2 && a[1] == "run" {
		return run(ctx, a, logger)
	}
	args, err := parseArgs(a)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(args.basepath, 0o700); err != nil {
		return fmt.Errorf("failed to create basepath directory: %v", err)
	}

	d, err := clockdrawer.New(a[0], args.color, args.format, args.big)
	if err != nil {
		return err
	}

	logger.Infof("logging %s images to %s every %s with target size of 100MB\n", args.format, args.basepath, args.interval)
	for SelectContextOrWait(ctx, args.interval) {
		if err := writeImage(&d, args.basepath, args.targetSize, args.format); err != nil {
			return err
		}
	}
	return nil
}

type args struct {
	basepath   string
	color      color.NRGBA
	interval   time.Duration
	format     string
	big        bool
	targetSize int64 // Added parameter for target file size in MB
}

var colors = map[string]color.NRGBA{
	"white": {R: 255, G: 255, B: 255, A: 255},
	"red":   {R: 255, A: 255},
	"green": {G: 255, A: 255},
	"blue":  {B: 255, A: 255},
}

var colorOptions = maps.Keys(colors)

func parseArgs(a []string) (args, error) {
	if len(a) < 6 {
		return args{}, fmt.Errorf("usage: %s basepath color interval format size [targetSizeMB]", a[0])
	}
	basepath := a[1]

	c, ok := colors[a[2]]
	if !ok {
		return args{}, fmt.Errorf("unsupported color %s, color options: %s", a[2], strings.Join(colorOptions, " "))
	}
	interval, err := time.ParseDuration(a[3])
	if err != nil {
		return args{}, fmt.Errorf("invalid interval: %v", err)
	}
	format := a[4]
	size := a[5]
	if size != "big" && size != "small" {
		return args{}, fmt.Errorf("size is one of the supported options %s. supported sizes: big small", a[5])
	}
	big := size == "big"

	// Default target size is 100 MB
	targetSize := int64(100)
	if len(a) >= 7 {
		var parseErr error
		var tSize int
		tSize, parseErr = parseIntArg(a[6])
		if parseErr != nil {
			return args{}, fmt.Errorf("invalid target size: %v", parseErr)
		}
		targetSize = int64(tSize)
	}

	return args{
		basepath:   basepath,
		color:      c,
		interval:   interval,
		format:     format,
		big:        big,
		targetSize: targetSize,
	}, nil
}

func parseIntArg(arg string) (int, error) {
	var result int
	_, err := fmt.Sscanf(arg, "%d", &result)
	return result, err
}

// Direct encoding approach that doesn't require creating a giant in-memory image
func writeDirectLargeImage(w *bufio.Writer, baseImage image.Image, targetSizeMB int64, imgFormat string) error {
	// For JPEG, we can use streams directly
	if imgFormat == "jpeg" {
		return writeDirectLargeJPEG(w, baseImage, targetSizeMB)
	} else {
		return writeDirectLargePNG(w, baseImage, targetSizeMB)
	}
}

func writeDirectLargeJPEG(w *bufio.Writer, baseImage image.Image, targetSizeMB int64) error {
	// Create a JPEG encoder with quality settings
	options := &jpeg.Options{
		Quality: 100,
	}

	// Target size in bytes
	targetBytes := targetSizeMB * 1024 * 1024

	// Get original dimensions
	origBounds := baseImage.Bounds()
	origWidth := origBounds.Dx()
	origHeight := origBounds.Dy()

	// Calculate a reasonable dimension for our intermediate image
	// Start small - we can always add more data after encoding
	scaleFactor := 2
	newWidth := origWidth * scaleFactor
	newHeight := origHeight * scaleFactor

	// Create a slightly larger image that we can handle in memory
	newImg := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))

	// Fill with random data to prevent good compression
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	for y := 0; y < newHeight; y++ {
		for x := 0; x < newWidth; x++ {
			// Sample from original image (repeated)
			r, g, b, a := baseImage.At(x%origWidth, y%origHeight).RGBA()

			// Add noise to prevent good compression
			noise := uint8(rnd.Intn(10))
			newImg.SetRGBA(x, y, color.RGBA{
				R: uint8(r>>8) + noise,
				G: uint8(g>>8) + noise,
				B: uint8(b>>8) + noise,
				A: uint8(a >> 8),
			})
		}
	}

	// First, create a buffer to hold one copy of the JPEG data
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, newImg, options); err != nil {
		return fmt.Errorf("failed to encode JPEG: %v", err)
	}

	baseImageSize := buf.Len()
	fmt.Printf("Base JPEG image size: %d bytes\n", baseImageSize)

	// Write the initial JPEG data
	if _, err := w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("failed to write JPEG data: %v", err)
	}

	// Calculate remaining bytes needed to reach target
	remainingBytes := targetBytes - int64(baseImageSize)
	if remainingBytes <= 0 {
		fmt.Printf("Base image already exceeds target size, no padding needed\n")
		return nil
	}

	fmt.Printf("Adding %d bytes of JPEG padding to reach target size of %d MB\n",
		remainingBytes, targetSizeMB)

	// Add padding data in smaller chunks to avoid overwhelming memory
	// JPEG comment marker starts with 0xFF 0xFE
	commentMarker := []byte{0xFF, 0xFE}

	// JPEG comment size is limited to 65535 bytes including the 2 length bytes
	maxCommentSize := 65533 // 65535 - 2 bytes for length
	chunkSize := maxCommentSize
	if chunkSize > 1024*1024 {
		chunkSize = 1024 * 1024 // Cap at 1MB per chunk for safety
	}

	bytesAdded := int64(0)
	for bytesAdded < remainingBytes {
		// Determine size of this chunk
		thisChunkSize := chunkSize
		if remainingBytes-bytesAdded < int64(chunkSize) {
			thisChunkSize = int(remainingBytes - bytesAdded)
		}

		// Maximum size check for JPEG comment
		if thisChunkSize > maxCommentSize {
			thisChunkSize = maxCommentSize
		}

		// Write comment marker
		if _, err := w.Write(commentMarker); err != nil {
			return fmt.Errorf("failed to write comment marker: %v", err)
		}
		bytesAdded += 2

		// Write length (includes the length bytes themselves, so +2)
		lengthBytes := []byte{byte((thisChunkSize + 2) >> 8), byte(thisChunkSize + 2)}
		if _, err := w.Write(lengthBytes); err != nil {
			return fmt.Errorf("failed to write comment length: %v", err)
		}
		bytesAdded += 2

		// Write random data as comment content
		paddingData := make([]byte, thisChunkSize)
		rnd.Read(paddingData)
		if _, err := w.Write(paddingData); err != nil {
			return fmt.Errorf("failed to write padding data: %v", err)
		}
		bytesAdded += int64(thisChunkSize)

		// Log progress periodically
		if bytesAdded%(10*1024*1024) == 0 { // Every 10MB
			fmt.Printf("Added %d MB of padding data so far\n", bytesAdded/(1024*1024))
		}
	}

	fmt.Printf("Total JPEG padding added: %d bytes\n", bytesAdded)
	return nil
}

func writeDirectLargePNG(w *bufio.Writer, baseImage image.Image, targetSizeMB int64) error {
	// Target size in bytes
	targetBytes := targetSizeMB * 1024 * 1024

	// Get original dimensions
	origBounds := baseImage.Bounds()
	origWidth := origBounds.Dx()
	origHeight := origBounds.Dy()

	// Use a modest scale factor to create a base image
	scaleFactor := 2
	newWidth := origWidth * scaleFactor
	newHeight := origHeight * scaleFactor

	// Create a new image with the scaled dimensions
	newImg := image.NewRGBA(image.Rect(0, 0, newWidth, newHeight))

	// Fill with uncompressible data
	rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
	for y := 0; y < newHeight; y++ {
		for x := 0; x < newWidth; x++ {
			// Sample from original image (repeated)
			origX, origY := x%origWidth, y%origHeight
			r, g, b, a := baseImage.At(origX, origY).RGBA()

			// Add some noise to make it less compressible
			noiseR := uint8(rnd.Intn(10))
			noiseG := uint8(rnd.Intn(10))
			noiseB := uint8(rnd.Intn(10))

			newImg.SetRGBA(x, y, color.RGBA{
				R: uint8(r>>8) ^ noiseR,
				G: uint8(g>>8) ^ noiseG,
				B: uint8(b>>8) ^ noiseB,
				A: uint8(a >> 8),
			})
		}
	}

	// Encode the PNG to a buffer
	var buf bytes.Buffer
	encoder := &png.Encoder{
		CompressionLevel: png.NoCompression, // Use no compression to maximize size
	}
	if err := encoder.Encode(&buf, newImg); err != nil {
		return fmt.Errorf("failed to encode PNG: %v", err)
	}

	baseImageSize := buf.Len()
	fmt.Printf("Base PNG image size: %d bytes\n", baseImageSize)

	// Write the PNG data
	if _, err := w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("failed to write PNG data: %v", err)
	}

	// Calculate how much additional data to write to reach the target size
	remainingBytes := targetBytes - int64(baseImageSize)
	if remainingBytes <= 0 {
		fmt.Printf("Base image already exceeds target size, no padding needed\n")
		return nil
	}

	fmt.Printf("Adding %d bytes of custom PNG chunks to reach target size of %d MB\n",
		remainingBytes, targetSizeMB)

	// If we need to add more data, we'll add it as additional PNG chunks
	// PNG chunk format: 4-byte length, 4-byte type, data, 4-byte CRC
	chunkType := []byte("tEXt") // Using text chunk type

	// Instead of one large chunk, use many smaller chunks
	// Each chunk will have overhead of 12 bytes (4 length + 4 type + 4 CRC)
	chunkSize := 1024 * 1024 // 1MB per chunk

	bytesAdded := int64(0)
	for bytesAdded < remainingBytes {
		// Determine how much data this chunk can contain
		// We need to account for the 12 bytes of overhead
		thisChunkDataSize := chunkSize
		if remainingBytes-bytesAdded < int64(chunkSize+12) {
			thisChunkDataSize = int(remainingBytes - bytesAdded - 12)
			if thisChunkDataSize <= 0 {
				// Not enough room for another chunk with overhead
				break
			}
		}

		// Create chunk data with random bytes
		chunkData := make([]byte, thisChunkDataSize)
		rnd.Read(chunkData)

		// Write length (4 bytes, big-endian)
		lengthBytes := []byte{
			byte(thisChunkDataSize >> 24),
			byte(thisChunkDataSize >> 16),
			byte(thisChunkDataSize >> 8),
			byte(thisChunkDataSize),
		}
		w.Write(lengthBytes)
		bytesAdded += 4

		// Write chunk type
		w.Write(chunkType)
		bytesAdded += 4

		// Write chunk data
		w.Write(chunkData)
		bytesAdded += int64(thisChunkDataSize)

		// Calculate CRC (over type and data)
		crc := crc32.NewIEEE()
		crc.Write(chunkType)
		crc.Write(chunkData)
		crcValue := crc.Sum32()

		// Write CRC (4 bytes, big-endian)
		crcBytes := []byte{
			byte(crcValue >> 24),
			byte(crcValue >> 16),
			byte(crcValue >> 8),
			byte(crcValue),
		}
		w.Write(crcBytes)
		bytesAdded += 4

		// Log progress periodically
		if bytesAdded%(10*1024*1024) == 0 { // Every 10MB
			fmt.Printf("Added %d MB of padding data so far\n", bytesAdded/(1024*1024))
		}
	}

	// Add the IEND chunk to properly terminate the PNG file
	// IEND chunk has length 0, type "IEND", no data, and CRC 0xAE426082
	w.Write([]byte{0, 0, 0, 0})             // Length 0
	w.Write([]byte("IEND"))                 // Type IEND
	w.Write([]byte{0xAE, 0x42, 0x60, 0x82}) // CRC

	fmt.Printf("Total PNG padding added: %d bytes\n", bytesAdded)
	return nil
}

func writeImage(cd *clockdrawer.ClockDrawer, basepath string, targetSizeMB int64, format string) error {
	nowStr := time.Now().Format(time.RFC3339Nano)
	nowStr = strings.ReplaceAll(nowStr, ":", "_")
	baseImage, err := cd.Image("time: " + nowStr)
	if err != nil {
		return fmt.Errorf("failed to create image: %v", err)
	}

	f, err := os.Create(filepath.Join(basepath, nowStr+cd.Ext()))
	if err != nil {
		return fmt.Errorf("failed to create file: %v", err)
	}
	defer f.Close()

	b := bufio.NewWriter(f)

	// Use the direct streaming approach to create a large file
	if err := writeDirectLargeImage(b, baseImage, targetSizeMB, cd.Format); err != nil {
		return fmt.Errorf("failed to create large image: %v", err)
	}

	if err := b.Flush(); err != nil {
		return fmt.Errorf("failed to flush buffer: %v", err)
	}

	fileInfo, err := f.Stat()
	if err != nil {
		return fmt.Errorf("failed to get file info: %v", err)
	}

	fileSizeMB := float64(fileInfo.Size()) / (1024 * 1024)
	fmt.Printf("Created image: %s (%.2f MB)\n", filepath.Join(basepath, nowStr+cd.Ext()), fileSizeMB)

	return nil
}

func SelectContextOrWait(ctx context.Context, duration time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(duration):
		return true
	}
}
