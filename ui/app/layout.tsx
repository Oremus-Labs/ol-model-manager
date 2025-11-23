import type { Metadata } from 'next';
import { Inter } from 'next/font/google';
import './globals.css';
import { Shell } from '@/components/layout/shell';

const inter = Inter({ subsets: ['latin'], variable: '--font-inter' });

export const metadata: Metadata = {
  title: 'Model Manager | Oremus Labs',
  description: 'Operational console for provisioning and curating large language models on the Venus cluster.',
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body className={`${inter.variable} bg-slate-950 text-slate-100 min-h-screen`}>
        <Shell>{children}</Shell>
      </body>
    </html>
  );
}
