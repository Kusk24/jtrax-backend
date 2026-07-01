import express, { type Request, type Response } from 'express';

const app = express();
const PORT = Number(process.env.PORT) || 3000;

app.use(express.json());

app.get('/health', (_req: Request, res: Response) => {
  res.json({ status: 'ok', service: 'jtrax-backend' });
});

app.listen(PORT, () => {
  console.log(`jtrax-backend listening on http://localhost:${PORT}`);
});
