import { SystemOverview } from '@/components/sections/system-overview';
import { Section } from '@/components/layout/section';
import { InstallWeightsForm } from '@/components/forms/install-weights-form';
import { ModelsPanel } from '@/components/sections/models-panel';
import { WeightsPanel } from '@/components/sections/weights-panel';
import { JobsPanel } from '@/components/sections/jobs-panel';
import { HistoryPanel } from '@/components/sections/history-panel';
import { ArchitecturesPanel } from '@/components/sections/architectures-panel';
import { getArchitectures, getHistory, getJobs, getModels, getSystemInfo, getWeights } from '@/lib/api';

export const dynamic = 'force-dynamic';

export default async function Page() {
  const [systemInfo, models, weights, jobs, history, architectures] = await Promise.all([
    getSystemInfo(),
    getModels(),
    getWeights(),
    getJobs(8),
    getHistory(8),
    getArchitectures(),
  ]);

  return (
    <main className="min-h-screen bg-gradient-to-b from-slate-950 to-slate-900 pb-16">
      <div className="mx-auto flex max-w-6xl flex-col gap-8 px-4 py-12 md:px-8">
        <header className="space-y-4 text-center">
          <p className="text-xs uppercase tracking-[0.3em] text-slate-400">Platform Intelligence</p>
          <h1 className="text-4xl font-bold text-white md:text-5xl">
            Model Manager Control Room
          </h1>
          <p className="text-base text-slate-300 md:text-lg">
            Observe, pre-stage, and activate large language models on the Venus GPU node with zero guesswork.
          </p>
        </header>

        <Section title="System Overview" description="Live insights pulled directly from the Model Manager API">
          <SystemOverview data={systemInfo} />
        </Section>

        <Section
          title="Install new weights"
          description="Pre-stage Hugging Face models on the Venus PVC to dodge cold starts"
        >
          <InstallWeightsForm />
        </Section>

        <Section title="Catalog entries" description="Generated from the Git-synced model catalog">
          <ModelsPanel models={models} />
        </Section>

        <Section title="Cached weights" description="Everything sitting on /mnt/models right now">
          <WeightsPanel weights={weights} />
        </Section>

        <Section title="Async jobs" description="Weight installs and other background jobs">
          <JobsPanel jobs={jobs} />
        </Section>

        <Section title="Activity timeline" description="Recent installs, activations, and deletes">
          <HistoryPanel events={history} />
        </Section>

        <Section title="vLLM coverage" description="Architectures scraped from upstream GitHub">
          <ArchitecturesPanel items={architectures} />
        </Section>
      </div>
    </main>
  );
}
