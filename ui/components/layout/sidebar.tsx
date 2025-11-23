import Link from 'next/link';

const nav = [
  { href: '#hero', label: 'Dashboard' },
  { href: '#catalog', label: 'Catalog' },
  { href: '#weights', label: 'Weights' },
  { href: '#jobs', label: 'Jobs' },
  { href: '#architectures', label: 'vLLM Library' },
];

export function Sidebar() {
  return (
    <aside className="hidden w-64 flex-shrink-0 border-r border-white/5 bg-slate-950/70 p-6 lg:block">
      <div className="space-y-10">
        <div>
          <p className="text-[10px] uppercase tracking-[0.5em] text-slate-500">Oremus Labs</p>
          <Link href="#hero" className="text-2xl font-bold text-white">
            Model Manager
          </Link>
          <p className="text-xs text-slate-400">Control Room</p>
        </div>
        <nav className="space-y-1">
          {nav.map((item) => (
            <a
              key={item.href}
              href={item.href}
              className="flex items-center rounded-xl px-3 py-2 text-sm text-slate-300 transition hover:bg-white/5"
            >
              {item.label}
            </a>
          ))}
        </nav>
        <div className="rounded-2xl bg-gradient-to-br from-brand-500/20 to-purple-500/20 p-4 text-xs text-slate-300">
          <p className="font-semibold text-white">Need automation?</p>
          <p className="mt-1 text-slate-300">Tap the API tab inside the console to generate configs programmatically.</p>
        </div>
      </div>
    </aside>
  );
}
