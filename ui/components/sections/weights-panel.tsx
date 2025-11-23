import type { WeightInfo } from '@/lib/types';
import { DeleteWeightForm } from '../forms/delete-weight-form';
import { formatBytes, formatDate } from '@/lib/utils';

interface Props {
  weights: WeightInfo[];
}

export function WeightsPanel({ weights }: Props) {
  if (!weights.length) {
    return <p className="text-sm text-slate-300">No cached weights detected yet.</p>;
  }

  return (
    <div className="grid gap-4">
      {weights.map((weight) => (
        <div key={weight.name} className="rounded-2xl border border-white/5 bg-slate-900/30 p-4 md:flex md:items-center md:justify-between">
          <div className="space-y-1">
            <h4 className="text-lg font-semibold text-white">{weight.name}</h4>
            <p className="text-sm text-slate-300">
              {formatBytes(weight.sizeBytes)} • {weight.fileCount} files • Updated {formatDate(weight.modifiedTime)}
            </p>
            <p className="text-xs text-slate-400">{weight.path}</p>
          </div>
          <div className="mt-4 w-full md:mt-0 md:w-64">
            <DeleteWeightForm name={weight.name} />
          </div>
        </div>
      ))}
    </div>
  );
}
