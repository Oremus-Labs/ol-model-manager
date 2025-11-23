import type { ModelInsight } from '@/lib/types';
import { SystemOverview } from '@/components/sections/system-overview';
import { Section } from '@/components/layout/section';
import { InstallWeightsForm } from '@/components/forms/install-weights-form';
import { ModelsPanel } from '@/components/sections/models-panel';
import { WeightsPanel } from '@/components/sections/weights-panel';
import { JobsPanel } from '@/components/sections/jobs-panel';
import { HistoryPanel } from '@/components/sections/history-panel';
import { VLLMLibrary } from '@/components/sections/vllm-library';
import { QuickActions } from '@/components/sections/quick-actions';
import { HuggingFaceSearch } from '@/components/sections/hf-search';
import { getArchitectures, getHistory, getJobs, getModels, getSystemInfo, getWeights, searchHuggingFace } from '@/lib/api';

export const dynamic = 'force-dynamic';

type PageProps = {
  searchParams?: Record<string, string | string[] | undefined>;
};

const isTruthy = (value: string | string[] | undefined): boolean => {
  if (!value) return false;
  const raw = Array.isArray(value) ? value[0] : value;
  if (!raw) return false;
  const normalized = raw.toLowerCase();
  return normalized === '1' || normalized === 'true' || normalized === 'yes' || normalized === 'on';
};

export default async function Page({ searchParams }: PageProps) {
  const queryParam = typeof searchParams?.q === 'string' ? searchParams.q : '';
  const compatibleOnly = isTruthy(searchParams?.compatibleOnly ?? searchParams?.compatible);

  const searchPromise = queryParam
    ? searchHuggingFace({ query: queryParam, compatibleOnly })
    : Promise.resolve<ModelInsight[] | null>(null);

  const [systemInfo, models, weights, jobs, history, architectures, searchResults] = await Promise.all([
    getSystemInfo(),
    getModels(),
    getWeights(),
    getJobs(8),
    getHistory(8),
    getArchitectures(),
    searchPromise,
  ]);

  return (
    <div className="space-y-8 pb-16">
      <section id="hero" className="space-y-4 text-left">
        <p className="text-xs uppercase tracking-[0.6em] text-slate-400">Platform Intelligence</p>
        <h1 className="text-4xl font-bold text-white md:text-5xl">Model Manager Control Room</h1>
        <p className="text-base text-slate-300 md:text-lg">
          Observe, pre-stage, and activate large language models on the Venus GPU fleet with zero guesswork.
        </p>
      </section>

      <QuickActions />

      <Section title="System Overview" description="Live telemetry from the Model Manager API" id="system">
        <SystemOverview data={systemInfo} />
      </Section>

      <Section
        title="Install new weights"
        description="Pre-stage Hugging Face models on the Venus PVC to dodge cold starts"
        id="weights"
      >
        <InstallWeightsForm />
      </Section>

      <Section
        title="Hugging Face search"
        description="Find compatible repos and inspect metadata before staging weights"
        id="discover"
      >
        <HuggingFaceSearch query={queryParam} compatibleOnly={compatibleOnly} results={searchResults} />
      </Section>

      <Section title="Catalog entries" description="Generated from the Git-synced model catalog" id="catalog">
        <ModelsPanel models={models} />
      </Section>

      <Section title="Cached weights" description="Everything sitting on /mnt/models right now">
        <WeightsPanel weights={weights} />
      </Section>

      <Section title="Async jobs" description="Weight installs and other background jobs" id="jobs">
        <JobsPanel jobs={jobs} />
      </Section>

      <Section title="Activity timeline" description="Recent installs, activations, and deletes">
        <HistoryPanel events={history} />
      </Section>

      <Section title="vLLM coverage" description="Architectures scraped from upstream GitHub">
        <VLLMLibrary items={architectures} />
      </Section>
    </div>
  );
}
