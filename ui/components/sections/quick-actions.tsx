const actions = [
  {
    title: 'Generate catalog entry',
    description: 'Use /catalog/generate to bootstrap JSON straight from Hugging Face metadata.',
    href: '#catalog',
  },
  {
    title: 'Pre-stage weights',
    description: 'Pull models onto the Venus PVC so runtime start-ups skip cold downloads.',
    href: '#weights',
  },
  {
    title: 'Submit PR automatically',
    description: 'POST /catalog/pr to save configs, branch, and open a GitHub PR in one shot.',
    href: 'https://github.com/Oremus-Labs/ol-model-manager/tree/main/README.md',
  },
];

export function QuickActions() {
  return (
    <div className="grid gap-4 lg:grid-cols-3">
      {actions.map((action) => (
        <a
          key={action.title}
          href={action.href}
          className="glass-panel block p-5 transition hover:-translate-y-1 hover:shadow-brand-500/20"
          target={action.href.startsWith('http') ? '_blank' : undefined}
          rel={action.href.startsWith('http') ? 'noreferrer' : undefined}
        >
          <p className="text-xs uppercase tracking-wide text-slate-400">Playbook</p>
          <h3 className="mt-1 text-lg font-semibold text-white">{action.title}</h3>
          <p className="mt-2 text-sm text-slate-300">{action.description}</p>
        </a>
      ))}
    </div>
  );
}
