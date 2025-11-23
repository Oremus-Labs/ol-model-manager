import { MetadataRoute } from 'next';

export default function manifest(): MetadataRoute.Manifest {
  return {
    name: 'Model Manager',
    short_name: 'ModelManager',
    start_url: '/',
    display: 'standalone',
    background_color: '#020617',
    description: 'Operational console for the Venus LLM cluster',
    icons: [
      {
        src: '/icon.svg',
        type: 'image/svg+xml',
        sizes: 'any',
      },
    ],
  };
}
