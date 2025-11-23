export function formatBytes(bytes?: number): string {
  if (!bytes || bytes <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / Math.pow(1024, index);
  return `${value.toFixed(value >= 10 ? 0 : 1)} ${units[index]}`;
}

export function formatDate(value?: string): string {
  if (!value) return '';
  return new Date(value).toLocaleString();
}

export function jobStatusColor(status: string): string {
  switch (status) {
    case 'completed':
      return 'bg-emerald-500/10 text-emerald-300';
    case 'running':
      return 'bg-blue-500/10 text-blue-300';
    case 'failed':
      return 'bg-rose-500/10 text-rose-300';
    default:
      return 'bg-slate-600/30 text-slate-200';
  }
}
