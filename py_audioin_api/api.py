import abc
from typing import Sequence

from grpclib.client import Channel
from grpclib.server import Stream

from viam.resource.types import RESOURCE_TYPE_COMPONENT, API
from viam.components.component_base import ComponentBase
from viam.resource.rpc_service_base import ResourceRPCServiceBase

from .grpc.audio_grpc import AudioServiceStub, AudioServiceBase
from .grpc.audio_pb2 import (
    GetAudioRequest,
    AudioChunk,
)

from viam.streams import StreamWithIterator

AudioStream = Stream[AudioChunk]

class Audio(ComponentBase):
    API = API("olivia", RESOURCE_TYPE_COMPONENT, "audio")

    @abc.abstractmethod
    async def get_audio(self, format: str, sampleRate: int, channels:int, durationSeconds: int, maxDurationSeconds: int, previousTimestamp:int) -> AudioStream: ...


class AudioRPCService(AudioServiceBase, ResourceRPCServiceBase):
    RESOURCE_TYPE = Audio

    async def GetAudio(self, stream: Stream[GetAudioRequest, AudioChunk]) -> None:
        return


class AudioClient(Audio):
    def __init__(self, name: str, channel: Channel) -> None:
        self.channel = channel
        self.client = AudioServiceStub(channel)
        super().__init__(name)

    async def get_audio(self, durationSeconds: int, codec:str, maxDurationSeconds: int, previousTimestamp:int) -> AudioStream:
        request = GetAudioRequest(name=self.name, duration_seconds=durationSeconds, codec = codec, max_duration_seconds= maxDurationSeconds, previous_timestamp = previousTimestamp)
        async def read():
            audio_stream: Stream[GetAudioRequest, AudioChunk]
            async with self.client.GetAudio.open() as audio_stream:
                try:
                    await audio_stream.send_message(request, end=True)
                    async for audioChunk in audio_stream:
                        yield audioChunk
                except Exception as e:
                    raise (e)

        return StreamWithIterator(read())
