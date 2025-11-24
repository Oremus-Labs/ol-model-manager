import { ReactNode } from 'react';
import { Sidebar } from './sidebar';

export function Shell({ children }: { children: ReactNode }) {
  return (
    <div className="app-shell">
      <Sidebar />
      <div className="flex min-h-screen w-full flex-col bg-gradient-to-b from-slate-950 via-slate-950 to-slate-900/90">
        <div className="flex-1 px-4 pb-16 pt-8 sm:px-8">
          <div className="mx-auto flex w-full max-w-6xl flex-col gap-8">{children}</div>
        </div>
      </div>
    </div>
  );
}
