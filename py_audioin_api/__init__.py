from viam.resource.registry import Registry, ResourceRegistration

from .api import AudioClient, Audio, AudioRPCService

Registry.register_api(
    ResourceRegistration(
        Audio,
        AudioRPCService,
        lambda name, channel: AudioClient(name, channel),
    )
)
