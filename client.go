package audio

// func main() {
// 	var opts []grpc.DialOption
// 	opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
// 	conn, err := grpc.NewClient("localhost:50051", opts[0])
// 	if err != nil {
// 		log.Fatalf("fail to dial: %v", err)
// 	}
// 	defer conn.Close()
// 	client := pb.NewAudioServiceClient(conn)

// 	// Create request for continuous audio (0 = indefinite)
// 	req := &pb.RecordRequest{
// 		Name:            "test",
// 		DurationSeconds: 0,
// 		Format:          "pcm",
// 		SampleRate:      44100,
// 		Channels:        1,
// 	}

// 	// Set up signal handling for graceful shutdown
// 	ctx, cancel := context.WithCancel(context.Background())
// 	defer cancel()

// 	sigChan := make(chan os.Signal, 1)
// 	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

// 	go func() {
// 		<-sigChan
// 		fmt.Println("\nReceived interrupt signal, stopping stream...")
// 		cancel()
// 	}()

// 	// Start streaming record
// 	stream, err := client.Record(ctx, req)
// 	if err != nil {
// 		log.Fatalf("failed to start recording: %v", err)
// 	}

// 	// Create output file to save streamed audio
// 	outputFile, err := os.Create("streamed_audio.raw")
// 	if err != nil {
// 		log.Fatalf("failed to create output file: %v", err)
// 	}
// 	defer outputFile.Close()

// 	fmt.Println("Receiving continuous audio stream... (Press Ctrl+C to stop)")
// 	chunkCount := 0

// 	// Receive streaming audio chunks continuously
// 	for {
// 		select {
// 		case <-ctx.Done():
// 			fmt.Println("Context cancelled, stopping stream...")
// 			return
// 		default:
// 			chunk, err := stream.Recv()
// 			if err == io.EOF {
// 				fmt.Println("Stream ended by server")
// 				break
// 			}
// 			if err != nil {
// 				log.Printf("Error receiving chunk: %v", err)
// 				break
// 			}

// 			// Write audio data to file
// 			if _, err := outputFile.Write(chunk.AudioData); err != nil {
// 				log.Printf("Error writing audio data: %v", err)
// 				break
// 			}

// 			chunkCount++
// 			fmt.Printf("Received chunk %d (sequence: %d, timestamp: %d, size: %d bytes)\n",
// 				chunkCount, chunk.Sequence, chunk.Timestamp, len(chunk.AudioData))
// 		}
// 	}
// }
