import { Navigate, Route, Routes } from 'react-router'
import MeetingPage from './MeetingPage'
import PersonasPage from './PersonasPage'
import './App.css'

// App is only the route table. Each page renders its own AppHeader, so a page
// can hand the header its own presence count without lifting agent state up.
function App() {
  return (
    <Routes>
      <Route path="/" element={<MeetingPage />} />
      <Route path="/personas" element={<PersonasPage />} />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}

export default App
