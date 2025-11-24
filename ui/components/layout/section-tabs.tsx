interface Tab {
  id: string;
  label: string;
}

const defaultTabs: Tab[] = [
  { id: 'active', label: 'Active' },
  { id: 'discover', label: 'Discovery' },
  { id: 'catalog', label: 'Catalog' },
  { id: 'weights', label: 'Weights' },
  { id: 'jobs', label: 'Jobs' },
  { id: 'history', label: 'Timeline' },
];

export function SectionTabs() {
  return (
    <nav className="no-scrollbar flex gap-2 overflow-x-auto rounded-2xl border border-white/5 bg-slate-950/40 p-2 text-sm text-slate-200">
      {defaultTabs.map((tab) => (
        <a
          key={tab.id}
          href={`#${tab.id}`}
          className="rounded-xl px-4 py-1.5 font-semibold text-slate-200 transition hover:bg-white/10"
        >
          {tab.label}
        </a>
      ))}
    </nav>
  );
}
