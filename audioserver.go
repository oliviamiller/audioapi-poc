package audio

import (
	"context"
	"fmt"
	"log"
	"net"

	"go.viam.com/rdk/logging"
	"go.viam.com/utils/rpc"
	"google.golang.org/grpc"

	"go.viam.com/rdk/resource"
	"go.viam.com/rdk/robot"

	pb "github.com/oliviamiller/audioapi-poc/grpc/audioin_api_go/grpc"
)

var API = resource.APINamespace("olivia").WithComponentType("audio")

// Named is a helper for getting the named Audioout's typed resource name.
func Named(name string) resource.Name {
	return resource.NewName(API, name)
}

func FromRobot(r robot.Robot, name string) (Audio, error) {
	return robot.ResourceFromRobot[Audio](r, Named(name))
}

func init() {
	resource.RegisterAPI(API, resource.APIRegistration[Audio]{
		// Reconfigurable, and contents of reconfwrapper.go are only needed for standalone (non-module) uses.
		RPCServiceServerConstructor: NewRPCServiceServer,
		RPCServiceHandler:           pb.RegisterAudioServiceHandlerFromEndpoint,
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

type AudioFormat int

const (
	Pcm16 AudioFormat = iota
	Pcm32
	Pcm32Float
	Mp3
)

type AudioInfo struct {
	Format     AudioFormat
	SampleRate int
	Channels   int
}

type Properties struct {
	SupportedFormats []AudioFormat
	maxChannels      int
}

type Audio interface {
	resource.Resource
	GetAudio(ctx context.Context, codec string, durationSeconds float32, max_duration float32, previous_timestamp int64) (<-chan *AudioChunk, error)
	Play(ctx context.Context, data []byte, codec string, sampleRate int, channels int) error
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

func (s *audioServer) GetAudio(req *pb.GetAudioRequest, stream pb.AudioService_GetAudioServer) error {
	fmt.Printf("Starting audio recording stream for %d seconds\n", req.DurationSeconds)

	// Get audio chunks from the resource
	a, err := s.coll.Resource(req.Name)
	if err != nil {
		return err
	}

	chunkChan, err := a.GetAudio(stream.Context(), req.Codec, req.DurationSeconds, req.MaxDurationSeconds, int64(req.PreviousTimestamp))
	if err != nil {
		return err
	}

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
			if chunk.Err != nil {
				return fmt.Errorf("audio capture error: %w", err)
			}
			// convert the chunk struct to a pb.audiochunk
			audioChunk := &pb.AudioChunk{
				AudioData: chunk.AudioData,
			}

			// Send chunk to client
			if err := stream.Send(audioChunk); err != nil {
				return fmt.Errorf("failed to send audio chunk: %w", err)
			}
		}
	}
}

func (s *audioServer) Play(ctx context.Context, req *pb.PlayRequest) (*pb.PlayResponse, error) {
	a, err := s.coll.Resource(req.Name)
	if err != nil {
		return nil, err
	}

	err = a.Play(ctx, req.AudioData, req.Info.Codec, int(req.Info.SampleRate), int(req.Info.NumChannels))
	if err != nil {
		return nil, err
	}
	return &pb.PlayResponse{}, nil

}

// func (s *audioServer) Properties(ctx context.Context, req *pb.PropertiesRequest) (*pb.PropertiesResponse, error) {
// 	// a, err := s.coll.Resource(req.Name)
// 	// if err != nil {
// 	// 	return nil, err
// 	// }

// 	// props, err := a.Properties(ctx)
// 	// if err != nil {
// 	// 	return nil, err
// 	// }
// 	return &pb.PropertiesResponse{}, nil
// 	// return &pb.PropertiesResponse{SupportedFormats: props.SupportedFormats, Channels: int32(props.maxChannels)}, nil

// }

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

func (c *audioClient) GetAudio(ctx context.Context, codec string, durationSeconds float32, max_duration float32, previous_timestamp int64) (<-chan *AudioChunk, error) {
	stream, err := c.client.GetAudio(ctx, &pb.GetAudioRequest{
		Name:               c.name,
		DurationSeconds:    durationSeconds,
		Codec:              codec,
		MaxDurationSeconds: max_duration,
		PreviousTimestamp:  float32(previous_timestamp),
	})

	if err != nil {
		return nil, err
	}

	ch := make(chan *AudioChunk)

	// Receive and process audio chunks
	go func() {
		defer close(ch)
		for {
			chunk, err := stream.Recv()
			if err != nil {
				if err.Error() != "EOF" {
					ch <- &AudioChunk{Err: err} // propagate error
				}
				fmt.Println("backgorund routine returning")
				fmt.Println(err.Error())
				return
			}

			ch <- &AudioChunk{
				AudioData: chunk.AudioData,
			}
		}
	}()

	fmt.Println("client is returning")
	return ch, nil
}

func (c *audioClient) Play(ctx context.Context, audio []byte, codec string, sampleRate int, channels int) error {

	info := &pb.AudioInfo{
		Codec:       codec,
		SampleRate:  int32(sampleRate),
		NumChannels: int32(channels),
	}
	_, err := c.client.Play(ctx, &pb.PlayRequest{
		Name:      c.name,
		AudioData: audio,
		Info:      info,
	})

	if err != nil {
		return err
	}

	return nil

}

// func (c *audioClient) Properties(ctx context.Context) error {
// 	props, err := c.client.Properties(ctx, &pb.PropertiesRequest{
// 		Name: c.name,
// 	})

// 	if err != nil {
// 		return err
// 	}

// 	return props, err
// }

func main() {
	lis, err := net.Listen("tcp", "localhost:50051")
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	pb.RegisterAudioServiceServer(grpcServer, newServer())
	fmt.Println("serving....")
	grpcServer.Serve(lis)
}
