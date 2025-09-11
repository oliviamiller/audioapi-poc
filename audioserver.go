package audio

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"time"

	"github.com/gordonklaus/portaudio"
	"go.viam.com/rdk/logging"
	"go.viam.com/utils/rpc"

	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/robot"

	pb "audiopoc/api/audio"
)

var API = resource.APINamespace("olivia").WithServiceType("audio")

// Named is a helper for getting the named Audioout's typed resource name.
func Named(name string) resource.Name {
	return resource.NewName(API, name)
}

// FromRobot is a helper for getting the named Speech from the given Robot.
func FromRobot(r robot.Robot, name string) (Audio, error) {
	return robot.ResourceFromRobot[Audio](r, Named(name))
}

func init() {
	resource.RegisterAPI(API, resource.APIRegistration[Audio]{
		// Reconfigurable, and contents of reconfwrapper.go are only needed for standalone (non-module) uses.
		RPCServiceServerConstructor: NewRPCServiceServer,
		RPCServiceHandler:           nil, // No HTTP gateway handler needed
		RPCServiceDesc:              &pb.AudioService_ServiceDesc,
		RPCClient: func(
			ctx context.Context,
			conn rpc.ClientConn,
			remoteName string,
			name resource.Name,
			logger logging.Logger,
		) (Audio, error) {
			return NewClientFromConn(conn, remoteName, name, logger), nil
		},
	})
}

type Audio interface {
	resource.Resource
	Record(ctx context.Context, durationSeconds int) (<-chan *AudioChunk, error)
}

type audioServer struct {
	pb.UnimplementedAudioServiceServer
	coll resource.APIResourceCollection[Audio]
}

// NewRPCServiceServer returns a new RPC server for the Audio API.
func NewRPCServiceServer(coll resource.APIResourceCollection[Audio]) interface{} {
	return &audioServer{coll: coll}
}

// WAV header structure
type wavHeader struct {
	ChunkID       [4]byte // "RIFF"
	ChunkSize     uint32  // File size - 8
	Format        [4]byte // "WAVE"
	Subchunk1ID   [4]byte // "fmt "
	Subchunk1Size uint32  // Size of format chunk (16 for PCM)
	AudioFormat   uint16  // 1 for PCM
	NumChannels   uint16  // Number of channels
	SampleRate    uint32  // Sample rate
	ByteRate      uint32  // Sample rate * num channels * bits per sample / 8
	BlockAlign    uint16  // Num channels * bits per sample / 8
	BitsPerSample uint16  // Bits per sample
	Subchunk2ID   [4]byte // "data"
	Subchunk2Size uint32  // Size of data
}

// writeWAVHeader writes a WAV header to the file
func writeWAVHeader(file *os.File, sampleRate, channels, bitsPerSample uint16, dataSize uint32) error {
	header := wavHeader{
		ChunkID:       [4]byte{'R', 'I', 'F', 'F'},
		ChunkSize:     36 + dataSize,
		Format:        [4]byte{'W', 'A', 'V', 'E'},
		Subchunk1ID:   [4]byte{'f', 'm', 't', ' '},
		Subchunk1Size: 16,
		AudioFormat:   1, // PCM
		NumChannels:   channels,
		SampleRate:    uint32(sampleRate),
		ByteRate:      uint32(sampleRate) * uint32(channels) * uint32(bitsPerSample) / 8,
		BlockAlign:    channels * bitsPerSample / 8,
		BitsPerSample: bitsPerSample,
		Subchunk2ID:   [4]byte{'d', 'a', 't', 'a'},
		Subchunk2Size: dataSize,
	}

	return binary.Write(file, binary.LittleEndian, header)
}

// lets pretend this is the module implementation for now

const sampleRate = 44100

// AudioCapturer handles audio capture and streaming via channels
type AudioCapturer struct {
	stream    *portaudio.Stream
	buffer    []float32
	sequence  int32
	isRunning bool
}

// NewAudioCapturer creates a new audio capturer
func NewAudioCapturer() *AudioCapturer {
	return &AudioCapturer{}
}

// StartCapture initializes audio capture and returns a channel of audio chunks
func (ac *AudioCapturer) StartCapture(ctx context.Context) (<-chan *pb.AudioChunk, <-chan error, error) {
	// Initialize PortAudio
	if err := portaudio.Initialize(); err != nil {
		return nil, nil, fmt.Errorf("failed to initialize PortAudio: %w", err)
	}

	// Audio parameters
	const framesPerBuffer = 1024
	const channels = 1 // Mono recording

	// Buffer to hold audio samples
	ac.buffer = make([]float32, framesPerBuffer*channels)

	// Open input stream
	var err error
	ac.stream, err = portaudio.OpenDefaultStream(
		channels,            // input channels
		0,                   // output channels (0 for input only)
		float64(sampleRate), // sample rate

		framesPerBuffer, // frames per buffer
		ac.buffer,       // buffer
	)
	if err != nil {
		portaudio.Terminate()
		return nil, nil, fmt.Errorf("failed to open stream: %w", err)
	}

	if err := ac.stream.Start(); err != nil {
		ac.stream.Close()
		portaudio.Terminate()
		return nil, nil, fmt.Errorf("failed to start stream: %w", err)
	}

	ac.sequence = 0
	ac.isRunning = true

	// Create channels for audio chunks and errors
	chunkChan := make(chan *pb.AudioChunk, 10) // Buffer for smoother streaming
	errorChan := make(chan error, 1)

	// Start goroutine to capture audio
	go ac.captureLoop(ctx, chunkChan, errorChan)

	return chunkChan, errorChan, nil
}

// captureLoop runs the audio capture loop
func (ac *AudioCapturer) captureLoop(ctx context.Context, chunkChan chan<- *pb.AudioChunk, errorChan chan<- error) {
	defer func() {
		close(chunkChan)
		close(errorChan)
		ac.Stop()
	}()

	for ac.isRunning {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Read audio data
		if err := ac.stream.Read(); err != nil {
			select {
			case errorChan <- fmt.Errorf("failed to read stream: %w", err):
			case <-ctx.Done():
			}
			return
		}

		// Convert float32 samples to int16 PCM bytes
		pcmData := make([]byte, len(ac.buffer)*2) // 2 bytes per int16 sample
		for i, sample := range ac.buffer {
			// Clamp sample to valid range
			if sample > 1.0 {
				sample = 1.0
			} else if sample < -1.0 {
				sample = -1.0
			}
			// Convert float32 to int16
			intSample := int16(sample * 32767.0)
			// Write as little-endian bytes
			pcmData[i*2] = byte(intSample & 0xFF)
			pcmData[i*2+1] = byte((intSample >> 8) & 0xFF)
		}

		// Create audio chunk
		chunk := &pb.AudioChunk{
			AudioData: pcmData,
			Timestamp: time.Now().UnixMicro(),
			Sequence:  ac.sequence,
		}

		ac.sequence++

		// Send chunk to channel
		select {
		case chunkChan <- chunk:
		case <-ctx.Done():
			return
		}
	}
}

// Stop stops the audio capture
func (ac *AudioCapturer) Stop() {
	ac.isRunning = false
	if ac.stream != nil {
		ac.stream.Stop()
		ac.stream.Close()
		ac.stream = nil
	}
	portaudio.Terminate()
}

// GetAudioChunks is a factory function that can be used to get audio chunks from any source
// This makes it easy to replace the implementation later (e.g., reading from another process)
func GetAudioChunks(ctx context.Context) (<-chan *pb.AudioChunk, <-chan error, func(), error) {
	capturer := NewAudioCapturer()

	chunkChan, errorChan, err := capturer.StartCapture(ctx)
	if err != nil {
		return nil, nil, nil, err
	}

	// Return cleanup function
	cleanup := func() {
		capturer.Stop()
	}

	return chunkChan, errorChan, cleanup, nil
}

func captureAudio(filename string, durationSeconds int) error {
	fmt.Println("trying to capture audio")

	// Initialize PortAudio
	if err := portaudio.Initialize(); err != nil {
		return fmt.Errorf("failed to initialize PortAudio: %w", err)
	}
	defer portaudio.Terminate()

	// Audio parameters
	const framesPerBuffer = 1024
	const channels = 1 // Mono recording

	// Buffer to hold audio samples
	buffer := make([]float32, framesPerBuffer*channels)
	var recordedSamples []float32

	// Open input stream
	stream, err := portaudio.OpenDefaultStream(
		channels,            // input channels
		0,                   // output channels (0 for input only)
		float64(sampleRate), // sample rate
		framesPerBuffer,     // frames per buffer
		buffer,              // buffer
	)
	if err != nil {
		return fmt.Errorf("failed to open stream: %w", err)
	}
	defer stream.Close()

	if err := stream.Start(); err != nil {
		return fmt.Errorf("failed to start stream: %w", err)
	}
	fmt.Println("recording audio...")

	// Calculate number of reads needed
	numReads := (sampleRate * durationSeconds) / framesPerBuffer
	for i := 0; i < numReads; i++ {
		if err := stream.Read(); err != nil {
			return fmt.Errorf("failed to read stream: %w", err)
		}
		recordedSamples = append(recordedSamples, buffer...)
	}

	fmt.Println("stopping stream...")
	if err := stream.Stop(); err != nil {
		return fmt.Errorf("failed to stop stream: %w", err)
	}

	// Create output file
	file, err := os.Create(filename)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	// Write raw audio data as 16-bit PCM
	for _, sample := range recordedSamples {
		if sample != 0.0 {
		}
		// Clamp sample to valid range
		if sample > 1.0 {
			sample = 1.0
		} else if sample < -1.0 {
			sample = -1.0
		}
		// Convert float32 to int16
		intSample := int16(sample * 32767.0)
		if err := binary.Write(file, binary.LittleEndian, intSample); err != nil {
			return fmt.Errorf("failed to write sample: %w", err)
		}
	}

	fmt.Printf("Recorded %d samples to %s\n", len(recordedSamples), filename)
	return nil
}

func (s *audioServer) Record(req *pb.RecordRequest, stream pb.AudioService_RecordServer) error {
	fmt.Printf("Starting audio recording stream for %d seconds\n", req.DurationSeconds)

	// Get audio chunks from the factory function
	chunkChan, errorChan, cleanup, err := GetAudioChunks(stream.Context())
	if err != nil {
		return fmt.Errorf("failed to start audio capture: %w", err)
	}
	defer cleanup()

	// Calculate how many chunks to send (for duration limit)
	const framesPerBuffer = 1024
	chunksPerSecond := sampleRate / framesPerBuffer
	totalChunks := int(req.DurationSeconds) * chunksPerSecond
	chunksSent := 0

	// Handle continuous streaming (duration 0 means indefinite)
	isInfinite := req.DurationSeconds <= 0

	// Stream audio chunks
	for {
		select {
		case <-stream.Context().Done():
			fmt.Println("Client disconnected, stopping recording")
			return nil

		case chunk, ok := <-chunkChan:
			if !ok {
				fmt.Println("Audio capture channel closed")
				return nil
			}

			// Send chunk to client
			if err := stream.Send(chunk); err != nil {
				return fmt.Errorf("failed to send audio chunk: %w", err)
			}

			chunksSent++
			if !isInfinite && chunksSent >= totalChunks {
				fmt.Printf("Recording complete. Sent %d audio chunks\n", chunksSent)
				return nil
			}

		case err, ok := <-errorChan:
			if !ok {
				fmt.Println("Audio capture error channel closed")
				return nil
			}
			return fmt.Errorf("audio capture error: %w", err)
		}
	}
}

func (s *audioServer) StopRecord(ctx context.Context, req *pb.StopRecordRequest) (*pb.StopRecordResponse, error) {
	// g, err := s.coll.Resource(req.Name)
	// if err != nil {
	// 	return nil, err
	// }
	// err = g.Play(ctx, req.file_path)
	// if err != nil {
	// 	return nil, err
	// }
	return &pb.StopRecordResponse{}, nil

}

func (s *audioServer) Play(ctx context.Context, req *pb.PlayRequest) (*pb.PlayResponse, error) {
	return &pb.PlayResponse{}, nil
}

func (s *audioServer) StopPlay(ctx context.Context, req *pb.StopPlayRequest) (*pb.StopPlayResponse, error) {
	return &pb.StopPlayResponse{}, nil

}

func newServer() *audioServer {
	return &audioServer{}
}

type serviceClient struct {
	resource.Named
	resource.AlwaysRebuild
	resource.TriviallyCloseable
	client pb.AudioServiceClient
	logger logging.Logger
}

// NewClientFromConn creates a new Speech RPC client from an existing connection.
func NewClientFromConn(conn rpc.ClientConn, remoteName string, name resource.Name, logger logging.Logger) Audio {
	sc := newSvcClientFromConn(conn, remoteName, name, logger)
	return clientFromSvcClient(sc, name.ShortName())
}

func newSvcClientFromConn(conn rpc.ClientConn, remoteName string, name resource.Name, logger logging.Logger) *serviceClient {
	client := pb.NewAudioServiceClient(conn)
	sc := &serviceClient{
		Named:  name.PrependRemote(remoteName).AsNamed(),
		client: client,
		logger: logger,
	}
	return sc
}

type audioClient struct {
	*serviceClient
	name string
}

func clientFromSvcClient(sc *serviceClient, name string) Audio {
	return &audioClient{sc, name}
}

func (c *audioClient) Name() resource.Name {
	return Named(c.name)
}

func (c *audioClient) Reconfigure(ctx context.Context, deps resource.Dependencies, conf resource.Config) error {
	return nil
}

func (c *audioClient) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	return nil, nil
}

type AudioChunk struct {
	Sequence  int64
	AudioData []byte
	Err       error // send errors through the channel
}

func (c *audioClient) Record(ctx context.Context, durationSeconds int) (<-chan *AudioChunk, error) {
	stream, err := c.client.Record(ctx, &pb.RecordRequest{
		Name:            c.name,
		DurationSeconds: int32(durationSeconds),
		Format:          "pcm",
		SampleRate:      44100,
		Channels:        1,
	})
	if err != nil {
		return nil, err
	}

	ch := make(chan AudioChunk)

	// Receive and process audio chunks
	go func() {
		defer close(ch)
		for {
			chunk, err := stream.Recv()
			if err != nil {
				if err.Error() != "EOF" {
					ch <- AudioChunk{Err: err} // propagate error
				}
				return
			}

			ch <- AudioChunk{
				AudioData: chunk.AudioData,
			}
		}
	}()
	return ch, nil
}

// func main() {
// 	lis, err := net.Listen("tcp", "localhost:50051")
// 	if err != nil {
// 		log.Fatalf("failed to listen: %v", err)
// 	}

// 	grpcServer := grpc.NewServer()
// 	pb.RegisterAudioServiceServer(grpcServer, newServer())
// 	fmt.Println("serving....")
// 	grpcServer.Serve(lis)
// }
