import React, { useState, useEffect } from 'react';
import { Form, Input, Button, Card, Tabs, message, Space } from 'antd';
import { UserOutlined, LockOutlined, SafetyCertificateOutlined, MailOutlined } from '@ant-design/icons';
import { useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { authApi } from '../services';
import { useAuthStore } from '../stores/authStore';
import { startProactiveRefresh } from '../services/api';

const Login: React.FC = () => {
  const [loading, setLoading] = useState(false);
  const [ldapEnabled, setLdapEnabled] = useState(false);
  const [authType, setAuthType] = useState('local');
  const [isRegister, setIsRegister] = useState(false);
  const navigate = useNavigate();
  const { isAuthenticated, setAuth, setExpireAt } = useAuthStore();
  const { t } = useTranslation();

  useEffect(() => {
    if (isAuthenticated) {
      navigate('/admin/dashboard');
    }
    
    // Check if LDAP is enabled
    authApi.getConfig().then(res => {
      if (res.data.ldap_enabled) {
        setLdapEnabled(true);
      }
    }).catch(() => {});
  }, [isAuthenticated, navigate]);

  const handleLogin = async (values: any) => {
    setLoading(true);
    try {
      const res = await authApi.login(values.username, values.password, authType);
      setAuth(res.data.token, res.data.user);
      setExpireAt(res.data.expire_at || null);
      startProactiveRefresh(res.data.expire_at || null);
      message.success(t('auth.loginSuccess'));
      navigate('/admin/dashboard');
    } catch (error: any) {
      message.error(error.response?.data?.error || t('auth.loginFailed'));
    } finally {
      setLoading(false);
    }
  };

  const handleRegister = async (values: any) => {
    setLoading(true);
    try {
      await authApi.register({
        username: values.username,
        password: values.password,
        nickname: values.nickname,
        email: values.email,
      });
      message.success(t('auth.registerSuccess'));
      setIsRegister(false);
      setAuthType('local');
    } catch (error: any) {
      message.error(error.response?.data?.error || t('auth.registerFailed'));
    } finally {
      setLoading(false);
    }
  };

  const loginForm = (
    <Form
      name="login"
      onFinish={handleLogin}
      size="large"
      autoComplete="off"
    >
      <Form.Item
        name="username"
        rules={[{ required: true, message: t('auth.pleaseInputUsername') }]}
      >
        <Input 
          prefix={<UserOutlined />} 
          placeholder={t('auth.username')} 
        />
      </Form.Item>

      <Form.Item
        name="password"
        rules={[{ required: true, message: t('auth.pleaseInputPassword') }]}
      >
        <Input.Password 
          prefix={<LockOutlined />} 
          placeholder={t('auth.password')} 
        />
      </Form.Item>

      <Form.Item>
        <Button 
          type="primary" 
          htmlType="submit" 
          loading={loading}
          block
          style={{ height: 44 }}
        >
          {t('auth.login')}
        </Button>
      </Form.Item>
      
      <div style={{ textAlign: 'center', marginTop: 16 }}>
        <a onClick={() => setIsRegister(true)}>{t('auth.noAccount')} {t('auth.registerNow')}</a>
      </div>
    </Form>
  );

  const registerForm = (
    <Form
      name="register"
      onFinish={handleRegister}
      size="large"
      autoComplete="off"
    >
      <Form.Item
        name="username"
        rules={[
          { required: true, message: t('auth.pleaseInputUsername') },
          { min: 3, max: 50, message: t('auth.usernameLength') }
        ]}
      >
        <Input 
          prefix={<UserOutlined />} 
          placeholder={t('auth.username')} 
        />
      </Form.Item>

      <Form.Item
        name="password"
        rules={[
          { required: true, message: t('auth.pleaseInputPassword') },
          { min: 6, message: t('auth.passwordLength') }
        ]}
      >
        <Input.Password 
          prefix={<LockOutlined />} 
          placeholder={t('auth.password')} 
        />
      </Form.Item>

      <Form.Item
        name="nickname"
        rules={[{ required: true, message: t('auth.pleaseInputNickname') }]}
      >
        <Input 
          prefix={<UserOutlined />} 
          placeholder={t('auth.nickname')} 
        />
      </Form.Item>

      <Form.Item
        name="email"
        rules={[
          { required: true, message: t('auth.pleaseInputEmail') },
          { type: 'email', message: t('auth.emailInvalid') }
        ]}
      >
        <Input 
          prefix={<MailOutlined />} 
          placeholder={t('auth.email')} 
        />
      </Form.Item>

      <Form.Item>
        <Space direction="vertical" style={{ width: '100%' }}>
          <Button 
            type="primary" 
            htmlType="submit" 
            loading={loading}
            block
            style={{ height: 44 }}
          >
            {t('auth.register')}
          </Button>
          <Button 
            type="link" 
            onClick={() => setIsRegister(false)}
            block
          >
            {t('auth.backToLogin')}
          </Button>
        </Space>
      </Form.Item>
    </Form>
  );

  const tabItems = [
    {
      key: 'local',
      label: (
        <span>
          <UserOutlined />
          {t('auth.accountLogin')}
        </span>
      ),
      children: loginForm,
    },
  ];

  if (ldapEnabled && !isRegister) {
    tabItems.push({
      key: 'ldap',
      label: (
        <span>
          <SafetyCertificateOutlined />
          {t('auth.ldapLogin')}
        </span>
      ),
      children: loginForm,
    });
  }

  if (isRegister) {
    tabItems[0] = {
      key: 'register',
      label: (
        <span>
          <UserOutlined />
          {t('auth.registerAccount')}
        </span>
      ),
      children: registerForm,
    };
  }

  return (
    <div style={{
      minHeight: '100vh',
      display: 'flex',
      justifyContent: 'center',
      alignItems: 'center',
      background: 'linear-gradient(135deg, #1a1a2e 0%, #16213e 50%, #0f3460 100%)',
      padding: '16px',
    }}>
      <Card 
        className="login-card"
        style={{ 
          width: '100%',
          maxWidth: 420, 
          boxShadow: '0 8px 24px rgba(0,0,0,0.2)',
          borderRadius: 8,
        }}
      >
        <div style={{ textAlign: 'center', marginBottom: 32 }}>
          <div style={{ 
            fontSize: 32, 
            fontWeight: 700, 
            color: '#1890ff',
            marginBottom: 8 
          }}>
            <SafetyCertificateOutlined style={{ marginRight: 8 }} />
            CodeSentry
          </div>
          <div style={{ color: '#666', fontSize: 14 }}>
            {t('common.appDescription')}
          </div>
        </div>

        <Tabs 
          activeKey={authType}
          onChange={setAuthType}
          centered
          items={tabItems}
        />
      </Card>
    </div>
  );
};

export default Login;
