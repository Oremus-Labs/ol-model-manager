import { Search } from 'lucide-react';

export function TopBar() {
  return (
    <header className="sticky top-0 z-30 flex items-center justify-between border-b border-white/5 bg-slate-950/70 px-6 py-4 backdrop-blur">
      <form className="relative w-full max-w-md">
        <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-slate-500" />
        <input
          type="search"
          placeholder="Quick search (models, weights, jobs...)"
          className="w-full rounded-xl border border-slate-800 bg-slate-900/80 py-2 pl-10 pr-3 text-sm text-white placeholder:text-slate-500 focus:border-brand-500 focus:outline-none"
        />
      </form>
      <div className="ml-6 flex items-center gap-4">
        <div className="hidden text-right text-xs text-slate-400 md:block">
          <p className="text-slate-200">Platform Operator</p>
          <p>model-manager@oremuslabs.app</p>
        </div>
        <div className="flex h-10 w-10 items-center justify-center rounded-full bg-gradient-to-br from-brand-500 to-purple-500 text-sm font-semibold text-white">
          MM
        </div>
      </div>
    </header>
  );
}
