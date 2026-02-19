package notify

import (
	"testing"
)

func TestGenerateWAV(t *testing.T) {
	wav := GenerateWAV()

	// Check WAV header
	if string(wav[0:4]) != "RIFF" {
		t.Errorf("Expected RIFF header, got %s", string(wav[0:4]))
	}

	if string(wav[8:12]) != "WAVE" {
		t.Errorf("Expected WAVE format, got %s", string(wav[8:12]))
	}

	if string(wav[12:16]) != "fmt " {
		t.Errorf("Expected fmt chunk, got %s", string(wav[12:16]))
	}

	if string(wav[36:40]) != "data" {
		t.Errorf("Expected data chunk, got %s", string(wav[36:40]))
	}

	// Check format - should be PCM (1)
	format := uint16(wav[20]) | uint16(wav[21])<<8
	if format != 1 {
		t.Errorf("Expected PCM format (1), got %d", format)
	}

	// Check channels - should be mono (1)
	channels := uint16(wav[22]) | uint16(wav[23])<<8
	if channels != 1 {
		t.Errorf("Expected 1 channel (mono), got %d", channels)
	}

	// Check sample rate - should be 22050
	sampleRate := uint32(wav[24]) | uint32(wav[25])<<8 | uint32(wav[26])<<16 | uint32(wav[27])<<24
	if sampleRate != 22050 {
		t.Errorf("Expected sample rate 22050, got %d", sampleRate)
	}

	// Check bits per sample - should be 16
	bitsPerSample := uint16(wav[34]) | uint16(wav[35])<<8
	if bitsPerSample != 16 {
		t.Errorf("Expected 16 bits per sample, got %d", bitsPerSample)
	}

	// Check file size is reasonable (approximately 17KB for 0.4 second mono 22050Hz 16-bit)
	expectedSize := 44 + (22050 * 2 * 4 / 10) // header + (sampleRate * bytesPerSample * duration)
	if len(wav) < expectedSize-1000 || len(wav) > expectedSize+1000 {
		t.Errorf("Unexpected file size: got %d, expected approximately %d", len(wav), expectedSize)
	}
}

func TestCompletionSoundEmbedded(t *testing.T) {
	// Test that the completion sound is embedded and valid
	if len(completionSound) < 44 {
		t.Errorf("Embedded completion sound is too small: %d bytes", len(completionSound))
	}

	// Check WAV header
	if string(completionSound[0:4]) != "RIFF" {
		t.Errorf("Expected RIFF header in embedded sound, got %s", string(completionSound[0:4]))
	}

	if string(completionSound[8:12]) != "WAVE" {
		t.Errorf("Expected WAVE format in embedded sound, got %s", string(completionSound[8:12]))
	}
}

func TestWAVReader(t *testing.T) {
	data := []byte{1, 2, 3, 4, 5}
	reader := NewWAVReader(data)

	buf := make([]byte, 3)
	n, err := reader.Read(buf)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if n != 3 {
		t.Errorf("Expected to read 3 bytes, got %d", n)
	}
	if buf[0] != 1 || buf[1] != 2 || buf[2] != 3 {
		t.Errorf("Unexpected data: %v", buf)
	}

	// Read remaining
	n, err = reader.Read(buf)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if n != 2 {
		t.Errorf("Expected to read 2 bytes, got %d", n)
	}

	// Read past end
	n, err = reader.Read(buf)
	if err == nil {
		t.Errorf("Expected EOF error")
	}
	if n != 0 {
		t.Errorf("Expected to read 0 bytes at EOF, got %d", n)
	}
}

func TestNotifierSetEnabled(t *testing.T) {
	// We can't fully test GetNotifier without audio hardware,
	// but we can test the Notifier methods
	n := &Notifier{enabled: true}

	if !n.IsEnabled() {
		t.Error("Expected notifier to be enabled")
	}

	n.SetEnabled(false)
	if n.IsEnabled() {
		t.Error("Expected notifier to be disabled")
	}

	n.SetEnabled(true)
	if !n.IsEnabled() {
		t.Error("Expected notifier to be enabled after re-enabling")
	}
}
