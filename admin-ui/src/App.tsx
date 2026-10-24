import { useEffect, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { storage } from '@/lib/storage'
import { LOGOUT_EVENT } from '@/api/client'
import { Login } from '@/components/login'
import { Shell, usePage } from '@/components/shell'
import { Toaster } from '@/components/ui/surface'
import { AccountsPage } from '@/pages/accounts'
import { RequestsPage } from '@/pages/requests'
import { UsagePage } from '@/pages/usage'
import { KeysPage } from '@/pages/keys'

export default function App() {
  const queryClient = useQueryClient()
  const [loggedIn, setLoggedIn] = useState(() => Boolean(storage.getApiKey()))
  const [page, navigate] = usePage()

  const logout = () => {
    storage.removeApiKey()
    queryClient.clear()
    setLoggedIn(false)
  }

  useEffect(() => {
    const onLogout = () => {
      queryClient.clear()
      setLoggedIn(false)
    }
    window.addEventListener(LOGOUT_EVENT, onLogout)
    return () => window.removeEventListener(LOGOUT_EVENT, onLogout)
  }, [queryClient])

  return (
    <>
      {loggedIn ? (
        <Shell page={page} onNavigate={navigate} onLogout={logout}>
          {page === 'accounts' ? <AccountsPage /> : page === 'requests' ? <RequestsPage /> : page === 'usage' ? <UsagePage /> : <KeysPage />}
        </Shell>
      ) : (
        <Login onLogin={() => setLoggedIn(true)} />
      )}
      <Toaster />
    </>
  )
}
